package gql

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"hash/fnv"
	"sync"

	"github.com/fasthttp/websocket"
	"github.com/gofiber/fiber/v3"
	"github.com/valyala/fasthttp"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// Subscribe 订阅查询：基于WAL逻辑复制(CDC)的变更推送
// 订阅涉及的表发生变更时重执行查询，结果指纹变化才推送；
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

	w, err := my.cdc.watch(plan.tables)
	if err != nil {
		return nil, err
	}

	events := make(chan gqlReply, 1)
	go my.stream(ctx, plan, variables, w, events)
	return events, nil
}

// stream 订阅事件循环：首查推送，之后等待表变更唤醒
func (my *Executor) stream(ctx context.Context, plan *Plan, variables map[string]interface{}, w *watcher, events chan<- gqlReply) {
	defer close(events)
	defer my.cdc.unwatch(w)

	var last uint64
	for {
		if reply, changed := my.tick(ctx, plan, variables, &last); changed {
			select {
			case events <- reply:
			case <-ctx.Done():
				return
			}
		}
		select {
		case <-w.wake:
		case <-ctx.Done():
			return
		}
	}
}

// tick 单次轮询：结果指纹无变化时返回false
func (my *Executor) tick(ctx context.Context, plan *Plan, variables map[string]interface{}, last *uint64) (gqlReply, bool) {
	var r gqlReply

	data, err := my.fetch(ctx, plan, variables)
	if err != nil {
		if ctx.Err() != nil {
			return r, false
		}
		r.Errors = gqlerror.List{gqlerror.Wrap(err)}
		return r, true
	}

	digest := fnv.New64a()
	_, _ = digest.Write(data)
	sum := digest.Sum64()
	if sum == *last {
		return r, false
	}
	*last = sum

	// 订阅是公开API：始终解包为Data供程序化消费（变更推送频率低，非热路径）
	result, err := my.unpack(ctx, plan, data)
	if err != nil {
		r.Errors = gqlerror.List{gqlerror.Wrap(err)}
		return r, true
	}
	r.Data = result
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
	return upgrader.Upgrade(c.RequestCtx(), my.serveSocket)
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
func (my *Executor) serveSocket(conn *websocket.Conn) {
	ctx, cancel := context.WithCancel(context.Background())
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
