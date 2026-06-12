package gql

import (
	"context"
	"hash/fnv"
	"sync"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/gofiber/fiber/v3"
	"github.com/valyala/fasthttp"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// Subscribe 订阅查询：按间隔轮询重执行，结果变化时推送（graphjin同款方案）
// 首次立即推送当前结果，之后仅在结果指纹变化时推送；ctx取消后通道关闭
func (my *Executor) Subscribe(ctx context.Context, query string, variables map[string]interface{}, operationName string) (<-chan gqlReply, error) {
	plan, err := my.plan(query, operationName, variables)
	if err != nil {
		return nil, err
	}

	events := make(chan gqlReply, 1)
	go my.poll(ctx, plan, variables, events)
	return events, nil
}

// SetInterval 设置订阅轮询间隔（默认1秒）
func (my *Executor) SetInterval(interval time.Duration) {
	if interval > 0 {
		my.interval = interval
	}
}

// poll 订阅轮询循环：执行->指纹比对->推送
func (my *Executor) poll(ctx context.Context, plan *Plan, variables map[string]interface{}, events chan<- gqlReply) {
	defer close(events)

	ticker := time.NewTicker(my.interval)
	defer ticker.Stop()

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
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

// tick 单次轮询：结果指纹无变化时返回false
func (my *Executor) tick(ctx context.Context, plan *Plan, variables map[string]interface{}, last *uint64) (gqlReply, bool) {
	var r gqlReply

	data, _, err := my.fetch(ctx, plan, variables)
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

	result, err := my.unpack(ctx, plan, data)
	if err != nil {
		r.Errors = gqlerror.List{gqlerror.Wrap(err)}
		return r, true
	}
	r.Data = result
	return r, true
}

// ---------- graphql-transport-ws 协议 ----------

// wsMessage graphql-transport-ws 协议消息
type wsMessage struct {
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
func (my *socketSession) write(message wsMessage) error {
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
			if err := session.write(wsMessage{Type: wsConnectionAck}); err != nil {
				return
			}
		case wsPing:
			if err := session.write(wsMessage{Type: wsPong}); err != nil {
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
	payload, err := json.Marshal(message.Payload)
	if err == nil {
		err = json.Unmarshal(payload, &req)
	}
	if err != nil {
		_ = session.write(wsMessage{ID: message.ID, Type: wsError,
			Payload: gqlerror.List{gqlerror.Errorf("无效的subscribe载荷: %v", err)}})
		return
	}

	subCtx, stop := context.WithCancel(ctx)
	events, err := my.Subscribe(subCtx, req.Query, req.Variables, req.OperationName)
	if err != nil {
		stop()
		_ = session.write(wsMessage{ID: message.ID, Type: wsError, Payload: gqlerror.List{gqlerror.Wrap(err)}})
		return
	}

	session.mu.Lock()
	if _, exists := session.subs[message.ID]; exists {
		session.mu.Unlock()
		stop()
		_ = session.write(wsMessage{ID: message.ID, Type: wsError,
			Payload: gqlerror.List{gqlerror.Errorf("订阅ID重复: %s", message.ID)}})
		return
	}
	session.subs[message.ID] = stop
	session.mu.Unlock()

	go func() {
		defer stop()
		for reply := range events {
			if err := session.write(wsMessage{ID: message.ID, Type: wsNext, Payload: reply}); err != nil {
				return
			}
		}
		_ = session.write(wsMessage{ID: message.ID, Type: wsComplete})
	}()
}
