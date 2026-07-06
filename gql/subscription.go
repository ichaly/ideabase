package gql

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"hash"
	"hash/fnv"
	"sync"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/valyala/fasthttp"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// feed 共享订阅流：同构订阅（查询+变量+作用域）共用一次重查循环，结果扇出。
// 热点订阅的重查次数与订阅者数解耦：表变更时每份feed只打一条SQL
type feed struct {
	subs map[chan gqlReply]bool
	last gqlReply // 最近一次推送，后来的订阅者立即补发（免重查）
	live bool     // last是否已有效
	stop context.CancelFunc
}

// subscribe 订阅查询：基于WAL逻辑复制(CDC)的变更推送
// 订阅涉及的表发生变更时重执行查询，结果指纹变化才推送最新状态
// （慢消费者不阻塞其他订阅者，未消费的旧结果被最新结果顶替）；
// 首次立即推送当前结果；ctx取消后通道关闭
// 依赖数据库 wal_level=logical 与连接账号的REPLICATION权限
func (my *Executor) subscribe(ctx context.Context, query string, variables map[string]interface{}, operationName string) (<-chan gqlReply, error) {
	if my.cdc == nil {
		return nil, fmt.Errorf("订阅不可用：当前数据库没有注册CDC唤醒源或DSN不可用")
	}
	plan, err := my.plan(query, operationName, variables)
	if err != nil {
		return nil, err
	}

	// 键在plan之后计算：codec入参已还原，等价请求（如shortId与数字ID）归并到同一feed
	scope := scopeValues(ctx)
	key := feedKey(query, operationName, variables, scope)
	events := make(chan gqlReply, 1)

	my.feedMu.Lock()
	f := my.feeds[key]
	if f == nil {
		w, err := my.cdc.watch(plan.tables)
		if err != nil {
			my.feedMu.Unlock()
			return nil, err
		}
		fctx, stop := context.WithCancel(WithScope(context.Background(), scope))
		f = &feed{subs: make(map[chan gqlReply]bool), stop: stop}
		my.feeds[key] = f
		go my.stream(fctx, plan, variables, w, f)
	}
	f.subs[events] = true
	if f.live {
		events <- f.last // 补发最近结果：通道新建且容量1，必不阻塞
	}
	my.feedMu.Unlock()

	go func() { <-ctx.Done(); my.leave(key, f, events) }()
	return events, nil
}

// stream 共享流事件循环：首查推送，之后表变更唤醒重查，变化结果扇出给全部订阅者
func (my *Executor) stream(ctx context.Context, plan *Plan, variables map[string]interface{}, w *watcher, f *feed) {
	defer my.cdc.unwatch(w)

	var last uint64
	digest := fnv.New64a() // stream单goroutine持有，tick内Reset复用免每次分配
	for {
		if reply, changed := my.tick(ctx, plan, variables, digest, &last); changed {
			my.feedMu.Lock()
			f.last, f.live = reply, true
			for ch := range f.subs {
				push(ch, reply)
			}
			my.feedMu.Unlock()
		}
		select {
		case <-w.wake:
		case <-ctx.Done():
			return
		}
	}
}

// push 非阻塞推送：通道满则挤掉未消费的旧结果放入最新（订阅语义是最新状态而非事件流）
func push(ch chan gqlReply, reply gqlReply) {
	for {
		select {
		case ch <- reply:
			return
		default:
			select {
			case <-ch:
			default:
			}
		}
	}
}

// leave 订阅者退出：关闭其通道；最后一个退出时停止共享流并摘除
func (my *Executor) leave(key string, f *feed, ch chan gqlReply) {
	my.feedMu.Lock()
	delete(f.subs, ch)
	close(ch)
	if len(f.subs) == 0 {
		f.stop()
		if my.feeds[key] == f {
			delete(my.feeds, key)
		}
	}
	my.feedMu.Unlock()
}

// feedKey 共享流身份：查询+操作名+变量+作用域（map序列化按键排序，同构请求必得同键）
func feedKey(query, operation string, variables, scope map[string]any) string {
	v, _ := json.Marshal(variables)
	s, _ := json.Marshal(scope)
	return query + "\x00" + operation + "\x00" + string(v) + "\x00" + string(s)
}

// tick 表变更唤醒后重查一次：结果指纹无变化时返回false（不推送）
func (my *Executor) tick(ctx context.Context, plan *Plan, variables map[string]interface{}, digest hash.Hash64, last *uint64) (gqlReply, bool) {
	var r gqlReply

	data, err := my.fetch(ctx, plan, variables)
	if err != nil {
		if ctx.Err() != nil {
			return r, false
		}
		r.Errors = gqlerror.List{gqlerror.Wrap(err)}
		data = []byte(err.Error()) // 错误同样参与指纹：持续故障只推送一次，恢复或换错才再推
	}

	digest.Reset()
	_, _ = digest.Write(data)
	sum := digest.Sum64()
	if sum == *last {
		return r, false
	}
	*last = sum
	if r.Errors != nil {
		return r, true
	}

	// 订阅始终解包为Data供程序化消费（变更推送频率低，非热路径），
	// 懒解包残留的原始字节段就地物化，维持Data全map契约
	result, warnings, err := my.unpack(ctx, plan, data, variables)
	if err != nil {
		r.Errors = gqlerror.List{gqlerror.Wrap(err)}
		return r, true
	}
	materialize(result)
	r.Errors, r.Data = warnings, result
	return r, true
}

// ---------- graphql-transport-ws 协议 ----------

// wsMessage graphql-transport-ws 协议消息（出站Payload为任意值，入站为原始JSON延迟解析）
type wsMessage struct {
	ID      string             `json:"id,omitempty"`
	Type    string             `json:"type"`
	Payload stdjson.RawMessage `json:"payload,omitempty"`
}

// wsReply 出站消息
type wsReply struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Payload any    `json:"payload,omitempty"`
}

// 协议消息类型
const (
	wsConnectionInit = "connection_init"
	wsConnectionAck  = "connection_ack"
	wsPing           = "ping"
	wsPong           = "pong"
	wsSubscribe      = "subscribe"
	wsNext           = "next"
	wsError          = "error"
	wsComplete       = "complete"
)

var upgrader = websocket.FastHTTPUpgrader{
	Subprotocols:    []string{"graphql-transport-ws"},
	CheckOrigin:     func(*fasthttp.RequestCtx) bool { return true },
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

// Upgrade 框架无关的订阅入口：将连接升级为 graphql-transport-ws WebSocket。
// scope 为升级前提取的行级作用域（升级后请求ctx不可用，否则订阅丢失隔离）。
// 仅依赖 fasthttp（fiber 亦跑在 fasthttp 上，WS 无法退回 net/http）；fiber 绑定见 fiber.go。
func (my *Executor) Upgrade(rc *fasthttp.RequestCtx, scope map[string]any) error {
	return upgrader.Upgrade(rc, func(conn *websocket.Conn) {
		my.serveSocket(conn, scope)
	})
}

// socketSession 单个WebSocket连接的订阅会话
type socketSession struct {
	mu   sync.Mutex
	conn *websocket.Conn
	subs map[string]context.CancelFunc
}

// write 串行化并发写
func (my *socketSession) write(message wsReply) error {
	my.mu.Lock()
	defer my.mu.Unlock()
	return my.conn.WriteJSON(message)
}

// wsInitTimeout connection_init握手时限，超时按协议关4408（测试可缩短）
var wsInitTimeout = 5 * time.Second

// closeSocket 按graphql-transport-ws协议码关闭连接（4401/4408/4429/4409...）
func closeSocket(conn *websocket.Conn, code int, reason string) {
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
	_ = conn.Close()
}

// serveSocket 连接读循环：init/ack、subscribe、complete、ping/pong。
// 协议状态机（graphql-transport-ws）：subscribe必须在init/ack之后（否则4401），
// init超时4408、重复init4429、重复订阅ID4409——鉴权握手不可绕过
// scope 为HTTP升级阶段提取的行级作用域，注入连接ctx供订阅查询隔离
func (my *Executor) serveSocket(conn *websocket.Conn, scope map[string]any) {
	ctx, cancel := context.WithCancel(WithScope(context.Background(), scope))
	defer cancel()

	session := &socketSession{conn: conn, subs: make(map[string]context.CancelFunc)}
	defer func() {
		session.mu.Lock()
		for _, stop := range session.subs {
			if stop != nil {
				stop()
			}
		}
		session.mu.Unlock()
	}()

	ready := false // 已完成init/ack握手
	watchdog := time.AfterFunc(wsInitTimeout, func() {
		closeSocket(conn, 4408, "Connection initialisation timeout")
	})
	defer watchdog.Stop()

	for {
		var message wsMessage
		if err := conn.ReadJSON(&message); err != nil {
			return
		}

		switch message.Type {
		case wsConnectionInit:
			if ready {
				closeSocket(conn, 4429, "Too many initialisation requests")
				return
			}
			watchdog.Stop()
			ready = true
			if err := session.write(wsReply{Type: wsConnectionAck}); err != nil {
				return
			}
		case wsPing:
			if err := session.write(wsReply{Type: wsPong}); err != nil {
				return
			}
		case wsSubscribe:
			if !ready {
				closeSocket(conn, 4401, "Unauthorized")
				return
			}
			if message.ID == "" {
				closeSocket(conn, 4400, "subscribe消息缺少id")
				return
			}
			if !session.reserve(message.ID) {
				closeSocket(conn, 4409, "Subscriber for "+message.ID+" already exists")
				return
			}
			my.startSubscription(ctx, session, message)
		case wsComplete:
			session.mu.Lock()
			if stop, ok := session.subs[message.ID]; ok {
				if stop != nil {
					stop()
				}
				delete(session.subs, message.ID)
			}
			session.mu.Unlock()
		}
	}
}

// reserve 预占订阅ID（占位nil，startSubscription成功后填入真实stop），重复返回false
func (my *socketSession) reserve(id string) bool {
	my.mu.Lock()
	defer my.mu.Unlock()
	if _, exists := my.subs[id]; exists {
		return false
	}
	my.subs[id] = nil
	return true
}

// startSubscription 启动单个订阅：消费事件通道并推送next帧
// 订阅ID已由调用方reserve预占，失败路径须释放占位
func (my *Executor) startSubscription(ctx context.Context, session *socketSession, message wsMessage) {
	release := func() {
		session.mu.Lock()
		delete(session.subs, message.ID)
		session.mu.Unlock()
	}
	var req gqlQuery
	if err := json.Unmarshal(message.Payload, &req); err != nil {
		release()
		_ = session.write(wsReply{ID: message.ID, Type: wsError,
			Payload: gqlerror.List{gqlerror.Errorf("无效的subscribe载荷: %v", err)}})
		return
	}

	subCtx, stop := context.WithCancel(ctx)
	events, err := my.subscribe(subCtx, req.Query, req.Variables, req.OperationName)
	if err != nil {
		stop()
		release()
		_ = session.write(wsReply{ID: message.ID, Type: wsError, Payload: gqlerror.List{gqlerror.Wrap(err)}})
		return
	}

	session.mu.Lock()
	session.subs[message.ID] = stop
	session.mu.Unlock()

	go func() {
		defer stop()
		for reply := range events {
			if err := session.write(wsReply{ID: message.ID, Type: wsNext, Payload: reply}); err != nil {
				return
			}
		}
		_ = session.write(wsReply{ID: message.ID, Type: wsComplete})
	}()
}
