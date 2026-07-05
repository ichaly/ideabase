package gql

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"hash"
	"hash/fnv"
	"sync"

	"github.com/fasthttp/websocket"
	"github.com/gofiber/fiber/v3"
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

// Subscribe 订阅查询：基于WAL逻辑复制(CDC)的变更推送
// 订阅涉及的表发生变更时重执行查询，结果指纹变化才推送最新状态
// （慢消费者不阻塞其他订阅者，未消费的旧结果被最新结果顶替）；
// 首次立即推送当前结果；ctx取消后通道关闭
// 依赖数据库 wal_level=logical 与连接账号的REPLICATION权限
func (my *Executor) Subscribe(ctx context.Context, query string, variables map[string]interface{}, operationName string) (<-chan gqlReply, error) {
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

	// 订阅是公开API：始终解包为Data供程序化消费（变更推送频率低，非热路径）
	result, warnings, err := my.unpack(ctx, plan, data, variables)
	if err != nil {
		r.Errors = gqlerror.List{gqlerror.Wrap(err)}
		return r, true
	}
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

// SubscribeHandler 处理GraphQL订阅的WebSocket升级（graphql-transport-ws子协议）
func (my *Executor) SubscribeHandler(c fiber.Ctx) error {
	// 升级前提取行级作用域：WebSocket升级后fiber请求ctx不可用，否则订阅丢失隔离
	scope := scopeValues(c.Context())
	return upgrader.Upgrade(c.RequestCtx(), func(conn *websocket.Conn) {
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

// serveSocket 连接读循环：init/ack、subscribe、complete、ping/pong
// scope 为HTTP升级阶段提取的行级作用域，注入连接ctx供订阅查询隔离
func (my *Executor) serveSocket(conn *websocket.Conn, scope map[string]any) {
	ctx, cancel := context.WithCancel(WithScope(context.Background(), scope))
	defer cancel()

	session := &socketSession{conn: conn, subs: make(map[string]context.CancelFunc)}
	defer func() {
		session.mu.Lock()
		for _, stop := range session.subs {
			stop()
		}
		session.mu.Unlock()
	}()

	for {
		var message wsMessage
		if err := conn.ReadJSON(&message); err != nil {
			return
		}

		switch message.Type {
		case wsConnectionInit:
			if err := session.write(wsReply{Type: wsConnectionAck}); err != nil {
				return
			}
		case wsPing:
			if err := session.write(wsReply{Type: wsPong}); err != nil {
				return
			}
		case wsSubscribe:
			my.startSubscription(ctx, session, message)
		case wsComplete:
			session.mu.Lock()
			if stop, ok := session.subs[message.ID]; ok {
				stop()
				delete(session.subs, message.ID)
			}
			session.mu.Unlock()
		}
	}
}

// startSubscription 启动单个订阅：消费事件通道并推送next帧
func (my *Executor) startSubscription(ctx context.Context, session *socketSession, message wsMessage) {
	var req gqlQuery
	if err := json.Unmarshal(message.Payload, &req); err != nil {
		_ = session.write(wsReply{ID: message.ID, Type: wsError,
			Payload: gqlerror.List{gqlerror.Errorf("无效的subscribe载荷: %v", err)}})
		return
	}

	subCtx, stop := context.WithCancel(ctx)
	events, err := my.Subscribe(subCtx, req.Query, req.Variables, req.OperationName)
	if err != nil {
		stop()
		_ = session.write(wsReply{ID: message.ID, Type: wsError, Payload: gqlerror.List{gqlerror.Wrap(err)}})
		return
	}

	session.mu.Lock()
	if _, exists := session.subs[message.ID]; exists {
		session.mu.Unlock()
		stop()
		_ = session.write(wsReply{ID: message.ID, Type: wsError,
			Payload: gqlerror.List{gqlerror.Errorf("订阅ID重复: %s", message.ID)}})
		return
	}
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
