// WAL逻辑复制监听（CDC）：订阅的唤醒信号源
// 单条复制连接 + pgoutput内置插件 + 临时复制槽（断开自动删除，无WAL堆积风险）
// 只解码到表级粒度：哪张表变了就唤醒涉及该表的订阅，由订阅重查+指纹比对决定是否推送
package gql

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/rs/zerolog/log"
)

// notifier 订阅唤醒源接口：按表变更唤醒watcher
// 新数据库的CDC实现（如MySQL binlog）实现本接口并registerNotifier注册，
// 执行器与订阅链路零修改
type notifier interface {
	watch(tables []string) (*watcher, error)
	unwatch(w *watcher)
	invalidate(tables ...string)
	cdcState() (string, string)
	close() // 停止监听并释放连接（Engine.Close调用，须幂等）
}

func (my *listener) invalidate(tables ...string) { my.notify(tables...) }

const (
	CDCStateIdleValidated = "IdleValidated"
	CDCStateConnected     = "Connected"
	CDCStateDisconnected  = "Disconnected"
)

const (
	cdcStateDisconnected uint32 = iota
	cdcStateIdleValidated
	cdcStateConnected
)

// notifiers 唤醒源工厂注册表（key=数据库驱动名），在各实现文件的init中登记
var notifiers = map[string]func(db *Database, cfg internal.SubscriptionConfig) (notifier, error){}

// registerNotifier 注册唤醒源工厂
func registerNotifier(driver string, factory func(db *Database, cfg internal.SubscriptionConfig) (notifier, error)) {
	notifiers[driver] = factory
}

// 导入本包即注册PostgreSQL逻辑复制唤醒源
func init() {
	registerNotifier("postgres", newPostgresNotifier)
}

// newPostgresNotifier 构造PG唤醒源：DSN取subscription.dsn配置，缺省从数据库连接配置读取
func newPostgresNotifier(db *Database, cfg internal.SubscriptionConfig) (notifier, error) {
	dsn := strings.TrimSpace(cfg.DSN)
	if dsn == "" {
		dsn = db.DSN()
	}
	if dsn == "" {
		return nil, fmt.Errorf("无法获取数据库DSN，请配置 subscription.dsn")
	}
	listener := newListener(dsn, cfg.Publication)
	if err := listener.validate(); err != nil {
		listener.close()
		return nil, err
	}
	listener.setIdleValidated()
	return listener, nil
}

// watcher 单个订阅的唤醒端：关注的表集合 + 容量1的唤醒通道（天然合并连续变更）
type watcher struct {
	tables map[string]bool
	wake   chan struct{}
	status func() (bool, string)
	outage func() time.Duration
}

// listener WAL监听器：一个进程一条复制连接，表变更扇出给关注它的watcher
// watcher按表名索引，notify只触达关注该表的子集
type listener struct {
	mu       sync.Mutex
	started  bool                         // 复制连接是否已启动；失败不置位，下次Subscribe自动重试
	runWG    sync.WaitGroup               // run退出后才允许close返回
	watchers map[string]map[*watcher]bool // 表名 -> 关注该表的watcher集合

	ctx            context.Context // 监听器生命周期：close取消后接收与重连循环退出
	cancel         context.CancelFunc
	dsn            string
	publication    string
	connected      atomic.Bool
	state          atomic.Uint32
	disconnectedAt atomic.Int64
	lastError      atomic.Pointer[string]
}

// newListener 构造监听器（连接延迟到首个订阅时建立）
func newListener(dsn, publication string) *listener {
	if publication == "" {
		publication = "ideabase_cdc"
	}
	ctx, cancel := context.WithCancel(context.Background())
	listener := &listener{
		ctx:         ctx,
		cancel:      cancel,
		dsn:         dsn,
		publication: publication,
		watchers:    make(map[string]map[*watcher]bool),
	}
	listener.state.Store(cdcStateDisconnected)
	return listener
}

// close 停止监听：取消生命周期ctx，接收循环随ctx中断、重连循环退出
func (my *listener) close() {
	my.mu.Lock()
	my.setStatus(false, nil)
	my.cancel()
	my.mu.Unlock()
	my.runWG.Wait()
}

// watch 注册订阅的唤醒端；首次调用启动复制连接。
// 建连失败只返回错误不缓存（不用sync.Once），数据库恢复后的下一次订阅会自动重试
func (my *listener) watch(tables []string) (*watcher, error) {
	my.mu.Lock()
	if my.ctx.Err() != nil {
		my.mu.Unlock()
		return nil, fmt.Errorf("CDC监听已关闭")
	}
	if !my.started {
		conn, err := my.connect()
		if err != nil {
			my.setStatus(false, err)
			my.mu.Unlock()
			return nil, fmt.Errorf("CDC监听启动失败（确认 wal_level=logical 与REPLICATION权限）: %w", err)
		}
		if my.ctx.Err() != nil {
			my.closeConn(conn)
			my.mu.Unlock()
			return nil, fmt.Errorf("CDC监听已关闭")
		}
		my.started = true // 启动后run内部自带退避重连，无需再回退标志
		my.setStatus(true, nil)
		my.runWG.Add(1)
		go my.run(conn)
	}

	w := &watcher{tables: make(map[string]bool, len(tables)), wake: make(chan struct{}, 1), status: my.status, outage: my.outage}
	for _, table := range tables {
		w.tables[table] = true
		group := my.watchers[table]
		if group == nil {
			group = make(map[*watcher]bool)
			my.watchers[table] = group
		}
		group[w] = true
	}
	my.mu.Unlock()
	return w, nil
}

func (my *listener) validate() error {
	// Validate the complete replication path up front: publication-only checks
	// can pass while the first subscription later fails on slot/start rights.
	conn, err := my.connect()
	if err != nil {
		return fmt.Errorf("验证CDC复制连接失败: %w", err)
	}
	my.closeConn(conn)
	return nil
}

// unwatch 注销唤醒端
func (my *listener) unwatch(w *watcher) {
	my.mu.Lock()
	for table := range w.tables {
		if group := my.watchers[table]; group != nil {
			delete(group, w)
			if len(group) == 0 {
				delete(my.watchers, table)
			}
		}
	}
	my.mu.Unlock()
}

// notify 表变更扇出；tables为空表示广播（重连后补偿可能错过的变更）
func (my *listener) notify(tables ...string) {
	wake := func(w *watcher) {
		select {
		case w.wake <- struct{}{}:
		default: // 已有未消费的唤醒，合并
		}
	}

	my.mu.Lock()
	defer my.mu.Unlock()
	if len(tables) == 0 {
		for _, group := range my.watchers {
			for w := range group {
				wake(w)
			}
		}
		return
	}
	for _, table := range tables {
		for w := range my.watchers[table] {
			wake(w)
		}
	}
}

// connect 建立复制连接：确保发布存在、创建临时槽、启动逻辑复制
func (my *listener) connect() (*pgconn.PgConn, error) {
	if !validPublicationName(my.publication) {
		return nil, fmt.Errorf("CDC publication名称不安全: %q", my.publication)
	}
	ctx, cancel := context.WithTimeout(my.ctx, 15*time.Second)
	defer cancel()

	conn, err := pgconn.Connect(ctx, replicationDSN(my.dsn))
	if err != nil {
		return nil, fmt.Errorf("建立复制连接失败: %w", err)
	}
	fail := func(err error) (*pgconn.PgConn, error) {
		my.closeConn(conn)
		return nil, err
	}

	if err = my.ensurePublication(ctx, conn); err != nil {
		return fail(err)
	}

	slot, err := replicationSlotName()
	if err != nil {
		return fail(err)
	}
	created, err := pglogrepl.CreateReplicationSlot(ctx, conn, slot, "pgoutput",
		pglogrepl.CreateReplicationSlotOptions{Temporary: true})
	if err != nil {
		return fail(fmt.Errorf("创建临时复制槽失败: %w", err))
	}
	start, err := pglogrepl.ParseLSN(created.ConsistentPoint)
	if err != nil {
		return fail(fmt.Errorf("解析复制槽一致性位置失败: %w", err))
	}

	if err = pglogrepl.StartReplication(ctx, conn, slot, start, pglogrepl.StartReplicationOptions{
		PluginArgs: []string{"proto_version '1'", fmt.Sprintf("publication_names '%s'", my.publication)},
	}); err != nil {
		return fail(fmt.Errorf("启动逻辑复制失败: %w", err))
	}
	return conn, nil
}

func validPublicationName(name string) bool {
	if len(name) == 0 || len(name) > 63 || !identifierStart(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if c := name[i]; !identifierStart(c) && (c < '0' || c > '9') && c != '$' {
			return false
		}
	}
	return true
}

func identifierStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func (my *listener) ensurePublication(ctx context.Context, conn *pgconn.PgConn) error {
	if _, err := conn.Exec(ctx, `CREATE PUBLICATION "`+my.publication+`" FOR ALL TABLES`).ReadAll(); err != nil &&
		!strings.Contains(err.Error(), "42710") { // duplicate_object
		return fmt.Errorf("创建发布失败: %w", err)
	}
	return my.auditPublication(ctx, conn)
}

func replicationSlotName() (string, error) {
	id, err := randomTokenID()
	if err != nil {
		return "", fmt.Errorf("CDC replication slot identity: %w", err)
	}
	return "ideabase_" + id, nil
}

// auditPublication rejects narrow publications: an uncovered table would
// otherwise leave its subscriptions permanently stale without an error.
func (my *listener) auditPublication(ctx context.Context, conn *pgconn.PgConn) error {
	name := strings.ReplaceAll(my.publication, "'", "''")
	read := func(sql string) (string, bool) {
		results, err := conn.Exec(ctx, sql).ReadAll()
		if err != nil || len(results) == 0 || len(results[0].Rows) == 0 || len(results[0].Rows[0]) == 0 {
			return "", false
		}
		return string(results[0].Rows[0][0]), true
	}
	all, ok := read(fmt.Sprintf(`SELECT puballtables::text FROM pg_publication WHERE pubname = '%s'`, name))
	if !ok {
		return fmt.Errorf("无法审计CDC publication %q", my.publication)
	}
	if all == "t" || all == "true" {
		return nil
	}
	covered, _ := read(fmt.Sprintf(
		`SELECT COALESCE(string_agg(schemaname||'.'||tablename, ', ' ORDER BY tablename), '<空>')
		 FROM pg_publication_tables WHERE pubname = '%s'`, name))
	return fmt.Errorf("CDC publication %q must be FOR ALL TABLES (covered: %s)", my.publication, covered)
}

// run 接收循环：解码WAL消息到表级变更；断线退避重连，重连后广播补偿；close后退出
func (my *listener) run(conn *pgconn.PgConn) {
	defer my.runWG.Done()
	backoff := time.Second
	for {
		err := my.receive(conn)
		if err == nil {
			err = &publicFailure{"CDC replication connection lost", "SUBSCRIPTION_SOURCE_UNAVAILABLE"}
		}
		my.setStatus(false, err)
		my.notify() // wake existing feeds so they fail explicitly instead of going stale
		my.closeConn(conn)
		if my.ctx.Err() != nil {
			return
		}

		// 退避重连
		for {
			log.Warn().Dur("backoff", backoff).Msg("CDC复制连接断开，准备重连")
			delay := cdcReconnectDelay(backoff)
			select {
			case <-my.ctx.Done():
				return
			case <-time.After(delay):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			next, err := my.connect()
			if err != nil {
				my.setStatus(false, err)
				log.Error().Err(err).Msg("CDC重连失败")
				continue
			}
			conn, backoff = next, time.Second
			my.setStatus(true, nil)
			my.notify() // 广播：补偿断线期间可能错过的变更
			break
		}
	}
}

func cdcReconnectDelay(backoff time.Duration) time.Duration {
	if backoff <= 0 {
		return 0
	}
	return backoff/2 + time.Duration(rand.Int63n(int64(backoff/2)+1))
}

func (my *listener) setStatus(connected bool, err error) {
	my.connected.Store(connected)
	if err == nil {
		if connected {
			my.state.Store(cdcStateConnected)
		} else {
			my.state.Store(cdcStateDisconnected)
		}
		my.disconnectedAt.Store(0)
		my.lastError.Store(nil)
		return
	}
	my.state.Store(cdcStateDisconnected)
	my.disconnectedAt.CompareAndSwap(0, time.Now().UnixNano())
	message := err.Error()
	my.lastError.Store(&message)
}

func (my *listener) setIdleValidated() {
	my.connected.Store(true)
	my.state.Store(cdcStateIdleValidated)
	my.disconnectedAt.Store(0)
	my.lastError.Store(nil)
}

func (my *listener) outage() time.Duration {
	started := my.disconnectedAt.Load()
	if started == 0 {
		return 0
	}
	return time.Since(time.Unix(0, started))
}

func (my *listener) status() (bool, string) {
	if message := my.lastError.Load(); message != nil {
		return my.connected.Load(), *message
	}
	return my.connected.Load(), ""
}

func (my *listener) cdcState() (string, string) {
	state := CDCStateDisconnected
	switch my.state.Load() {
	case cdcStateIdleValidated:
		state = CDCStateIdleValidated
	case cdcStateConnected:
		state = CDCStateConnected
	}
	if message := my.lastError.Load(); message != nil {
		return state, *message
	}
	return state, ""
}

// receive 单连接接收循环，出错返回交由run重连
func (my *listener) receive(conn *pgconn.PgConn) error {
	position := pglogrepl.LSN(0)
	relations := make(map[uint32]string) // relation id -> 表名
	touched := make(map[string]bool)     // 当前事务内变更过的表，Commit时统一扇出
	deadline := time.Now().Add(statusInterval)

	for {
		// 周期上报消费进度，避免服务端断开
		if time.Now().After(deadline) {
			statusCtx, cancel := context.WithTimeout(my.ctx, cdcStatusTimeout)
			err := pglogrepl.SendStandbyStatusUpdate(statusCtx, conn,
				pglogrepl.StandbyStatusUpdate{WALWritePosition: position})
			if err != nil {
				cancel()
				return err
			}
			cancel()
			deadline = time.Now().Add(statusInterval)
		}

		ctx, cancel := context.WithDeadline(my.ctx, deadline)
		raw, err := conn.ReceiveMessage(ctx)
		cancel()
		if err != nil {
			if pgconn.Timeout(err) && my.ctx.Err() == nil {
				continue
			}
			return err
		}

		message, ok := raw.(*pgproto3.CopyData)
		if !ok || len(message.Data) == 0 {
			continue
		}

		switch message.Data[0] {
		case pglogrepl.PrimaryKeepaliveMessageByteID:
			keepalive, err := pglogrepl.ParsePrimaryKeepaliveMessage(message.Data[1:])
			if err != nil {
				return err
			}
			if keepalive.ServerWALEnd > position {
				position = keepalive.ServerWALEnd
			}
			if keepalive.ReplyRequested {
				deadline = time.Time{}
			}
		case pglogrepl.XLogDataByteID:
			data, err := pglogrepl.ParseXLogData(message.Data[1:])
			if err != nil {
				return err
			}
			if data.WALStart > position {
				position = data.WALStart
			}
			if err := my.dispatch(data.WALData, relations, touched); err != nil {
				return fmt.Errorf("decode pgoutput: %w", err)
			}
		}
	}
}

// dispatch 解码pgoutput消息：维护关系表映射，行变更累积到事务的touched集合，
// Commit时统一扇出——通知次数从行数降到每事务的去重表数，批量写入不放大
func (my *listener) dispatch(walData []byte, relations map[uint32]string, touched map[string]bool) error {
	message, err := pglogrepl.Parse(walData)
	if err != nil {
		return err
	}
	switch m := message.(type) {
	case *pglogrepl.RelationMessage:
		relations[m.RelationID] = subscriptionTableIdentity(m.Namespace, m.RelationName)
	case *pglogrepl.InsertMessage:
		if table := relations[m.RelationID]; table != "" {
			touched[table] = true
		}
	case *pglogrepl.UpdateMessage:
		if table := relations[m.RelationID]; table != "" {
			touched[table] = true
		}
	case *pglogrepl.DeleteMessage:
		if table := relations[m.RelationID]; table != "" {
			touched[table] = true
		}
	case *pglogrepl.TruncateMessage:
		for _, id := range m.RelationIDs {
			if table := relations[id]; table != "" {
				touched[table] = true
			}
		}
	case *pglogrepl.CommitMessage:
		if len(touched) > 0 {
			tables := make([]string, 0, len(touched))
			for table := range touched {
				tables = append(tables, table)
			}
			clear(touched) // 单次runtime清空替代逐项delete
			my.notify(tables...)
		}
	}
	return nil
}

// statusInterval 复制进度上报间隔
const statusInterval = 10 * time.Second
const cdcStatusTimeout = 5 * time.Second

func (my *listener) closeConn(conn *pgconn.PgConn) {
	ctx, cancel := context.WithTimeout(context.Background(), cdcStatusTimeout)
	defer cancel()
	_ = conn.Close(ctx)
}

// replicationDSN 在DSN上追加复制模式参数，兼容URL与key=value两种格式
func replicationDSN(dsn string) string {
	if strings.Contains(dsn, "://") {
		separator := "?"
		if strings.Contains(dsn, "?") {
			separator = "&"
		}
		return dsn + separator + "replication=database"
	}
	return dsn + " replication=database"
}
