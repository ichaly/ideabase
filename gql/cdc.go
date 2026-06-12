// WAL逻辑复制监听（CDC）：订阅的唤醒信号源
// 单条复制连接 + pgoutput内置插件 + 临时复制槽（断开自动删除，无WAL堆积风险）
// 只解码到表级粒度：哪张表变了就唤醒涉及该表的订阅，由订阅重查+指纹比对决定是否推送
package gql

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/rs/zerolog/log"
	gormpg "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// notifier 订阅唤醒源接口：按表变更唤醒watcher
// 新数据库的CDC实现（如MySQL binlog）实现本接口并registerNotifier注册，
// 执行器与订阅链路零修改
type notifier interface {
	watch(tables []string) (*watcher, error)
	unwatch(w *watcher)
}

// notifiers 唤醒源工厂注册表（key=gorm驱动名），在各实现文件的init中登记
var notifiers = map[string]func(db *gorm.DB, cfg internal.SubscriptionConfig) (notifier, error){}

// registerNotifier 注册唤醒源工厂
func registerNotifier(driver string, factory func(db *gorm.DB, cfg internal.SubscriptionConfig) (notifier, error)) {
	notifiers[driver] = factory
}

// 导入本包即注册PostgreSQL逻辑复制唤醒源
func init() {
	registerNotifier("postgres", newPostgresNotifier)
}

// newPostgresNotifier 构造PG唤醒源：DSN取subscription.dsn配置，缺省从gorm连接提取
func newPostgresNotifier(db *gorm.DB, cfg internal.SubscriptionConfig) (notifier, error) {
	dsn := strings.TrimSpace(cfg.DSN)
	if dsn == "" {
		if dialector, ok := db.Dialector.(*gormpg.Dialector); ok {
			dsn = dialector.Config.DSN
		}
	}
	if dsn == "" {
		return nil, fmt.Errorf("无法获取数据库DSN，请配置 subscription.dsn")
	}
	return newListener(dsn, cfg.Publication), nil
}

// watcher 单个订阅的唤醒端：关注的表集合 + 容量1的唤醒通道（天然合并连续变更）
type watcher struct {
	tables map[string]bool
	wake   chan struct{}
}

// listener WAL监听器：一个进程一条复制连接，表变更扇出给关注它的watcher
type listener struct {
	mu       sync.Mutex
	once     sync.Once
	err      error // 首次启动失败的原因，Subscribe据此报错
	watchers map[*watcher]bool

	dsn         string
	publication string
}

// newListener 构造监听器（连接延迟到首个订阅时建立）
func newListener(dsn, publication string) *listener {
	if publication == "" {
		publication = "ideabase_cdc"
	}
	return &listener{
		dsn:         dsn,
		publication: publication,
		watchers:    make(map[*watcher]bool),
	}
}

// watch 注册订阅的唤醒端；首次调用启动复制连接，失败则订阅失败
func (my *listener) watch(tables []string) (*watcher, error) {
	my.once.Do(func() {
		conn, err := my.connect()
		if err != nil {
			my.err = err
			return
		}
		go my.run(conn)
	})
	if my.err != nil {
		return nil, fmt.Errorf("CDC监听启动失败（确认 wal_level=logical 与REPLICATION权限）: %w", my.err)
	}

	w := &watcher{tables: make(map[string]bool, len(tables)), wake: make(chan struct{}, 1)}
	for _, table := range tables {
		w.tables[table] = true
	}
	my.mu.Lock()
	my.watchers[w] = true
	my.mu.Unlock()
	return w, nil
}

// unwatch 注销唤醒端
func (my *listener) unwatch(w *watcher) {
	my.mu.Lock()
	delete(my.watchers, w)
	my.mu.Unlock()
}

// notify 表变更扇出；table为空表示广播（重连后补偿可能错过的变更）
func (my *listener) notify(table string) {
	my.mu.Lock()
	defer my.mu.Unlock()
	for w := range my.watchers {
		if table == "" || w.tables[table] {
			select {
			case w.wake <- struct{}{}:
			default: // 已有未消费的唤醒，合并
			}
		}
	}
}

// connect 建立复制连接：确保发布存在、创建临时槽、启动逻辑复制
func (my *listener) connect() (*pgconn.PgConn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := pgconn.Connect(ctx, replicationDSN(my.dsn))
	if err != nil {
		return nil, fmt.Errorf("建立复制连接失败: %w", err)
	}
	fail := func(err error) (*pgconn.PgConn, error) {
		_ = conn.Close(context.Background())
		return nil, err
	}

	// 确保发布存在（已存在的报错忽略）
	if _, err = conn.Exec(ctx, fmt.Sprintf(
		`CREATE PUBLICATION %q FOR ALL TABLES`, my.publication)).ReadAll(); err != nil &&
		!strings.Contains(err.Error(), "42710") { // duplicate_object
		return fail(fmt.Errorf("创建发布失败: %w", err))
	}

	system, err := pglogrepl.IdentifySystem(ctx, conn)
	if err != nil {
		return fail(fmt.Errorf("IdentifySystem失败: %w", err))
	}

	slot := fmt.Sprintf("ideabase_%d_%d", os.Getpid(), time.Now().UnixNano()%100000)
	if _, err = pglogrepl.CreateReplicationSlot(ctx, conn, slot, "pgoutput",
		pglogrepl.CreateReplicationSlotOptions{Temporary: true}); err != nil {
		return fail(fmt.Errorf("创建临时复制槽失败: %w", err))
	}

	if err = pglogrepl.StartReplication(ctx, conn, slot, system.XLogPos, pglogrepl.StartReplicationOptions{
		PluginArgs: []string{"proto_version '1'", fmt.Sprintf("publication_names '%s'", my.publication)},
	}); err != nil {
		return fail(fmt.Errorf("启动逻辑复制失败: %w", err))
	}
	return conn, nil
}

// run 接收循环：解码WAL消息到表级变更；断线退避重连，重连后广播补偿
func (my *listener) run(conn *pgconn.PgConn) {
	backoff := time.Second
	for {
		my.receive(conn)
		_ = conn.Close(context.Background())

		// 退避重连
		for {
			log.Warn().Dur("backoff", backoff).Msg("CDC复制连接断开，准备重连")
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			next, err := my.connect()
			if err != nil {
				log.Error().Err(err).Msg("CDC重连失败")
				continue
			}
			conn, backoff = next, time.Second
			my.notify("") // 广播：补偿断线期间可能错过的变更
			break
		}
	}
}

// receive 单连接接收循环，出错返回交由run重连
func (my *listener) receive(conn *pgconn.PgConn) {
	position := pglogrepl.LSN(0)
	relations := make(map[uint32]string) // relation id -> 表名
	deadline := time.Now().Add(statusInterval)

	for {
		// 周期上报消费进度，避免服务端断开
		if time.Now().After(deadline) {
			if err := pglogrepl.SendStandbyStatusUpdate(context.Background(), conn,
				pglogrepl.StandbyStatusUpdate{WALWritePosition: position}); err != nil {
				return
			}
			deadline = time.Now().Add(statusInterval)
		}

		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		raw, err := conn.ReceiveMessage(ctx)
		cancel()
		if err != nil {
			if pgconn.Timeout(err) {
				continue
			}
			return
		}

		message, ok := raw.(*pgproto3.CopyData)
		if !ok || len(message.Data) == 0 {
			continue
		}

		switch message.Data[0] {
		case pglogrepl.PrimaryKeepaliveMessageByteID:
			keepalive, err := pglogrepl.ParsePrimaryKeepaliveMessage(message.Data[1:])
			if err != nil {
				return
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
				return
			}
			if data.WALStart > position {
				position = data.WALStart
			}
			my.dispatch(data.WALData, relations)
		}
	}
}

// dispatch 解码pgoutput消息：维护关系表映射，数据变更按表名扇出
func (my *listener) dispatch(walData []byte, relations map[uint32]string) {
	message, err := pglogrepl.Parse(walData)
	if err != nil {
		return
	}
	switch m := message.(type) {
	case *pglogrepl.RelationMessage:
		relations[m.RelationID] = m.RelationName
	case *pglogrepl.InsertMessage:
		my.notify(relations[m.RelationID])
	case *pglogrepl.UpdateMessage:
		my.notify(relations[m.RelationID])
	case *pglogrepl.DeleteMessage:
		my.notify(relations[m.RelationID])
	case *pglogrepl.TruncateMessage:
		for _, id := range m.RelationIDs {
			my.notify(relations[id])
		}
	}
}

// statusInterval 复制进度上报间隔
const statusInterval = 10 * time.Second

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

