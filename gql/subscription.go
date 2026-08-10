package gql

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"hash"
	"hash/fnv"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/valyala/fasthttp"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// feed 共享订阅流：同构订阅（查询+变量+作用域）共用一次重查循环，结果扇出。
// 热点订阅的重查次数与订阅者数解耦：表变更时每份feed只打一条SQL
type feed struct {
	key  string
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
func (my *Engine) subscribe(ctx context.Context, query string, variables map[string]interface{}, operationName string) (<-chan gqlReply, error) {
	if _, err := my.Snapshot(); err != nil {
		return nil, err
	}
	if variables == nil && strings.IndexByte(query, '$') >= 0 {
		variables = make(map[string]interface{})
	}
	view, err := my.effectiveSchema(ctx)
	if err != nil {
		return nil, err
	}
	plan, err := my.planWithView(view, query, operationName, variables)
	if err != nil {
		return nil, err
	}
	// subscribe is deliberately narrower than execute: once planning has
	// selected an operation, reject Query and Mutation before authorization,
	// CDC watch creation, or the first stream fetch.
	if plan.operation == nil || plan.operation.Operation != ast.Subscription {
		return nil, fmt.Errorf("订阅入口只接受subscription操作")
	}
	if my.cdc == nil {
		return nil, fmt.Errorf("订阅不可用：当前数据库没有注册CDC唤醒源或DSN不可用")
	}
	if my.revocation != nil {
		identity, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, fmt.Errorf("verified identity is required for subscription")
		}
		route, _ := PinnedRoute(ctx)
		if err := my.revocation.Check(identity, route); err != nil {
			return nil, err
		}
	}
	if my.policy != nil {
		ctx, err = my.authorizeProvider(ctx, plan.operation, variables)
		if err != nil {
			return nil, err
		}
		plan, err = my.policyPlan(ctx, plan, variables)
		if err != nil {
			return nil, err
		}
	}
	lease, _ := subscriptionLeaseFromContext(ctx)
	if lease.Barrier == nil {
		lease.Barrier = my.revocation
	}
	if identity, ok := IdentityFromContext(ctx); ok {
		lease.Identity = identity
		if lease.Interval <= 0 {
			lease.Interval = configuredSubscriptionLease(my)
		}
		if route, exists := PinnedRoute(ctx); exists {
			lease.Route = route
		}
	}
	lease.PolicyProvider, lease.Operation, lease.Variables = my.policy, plan.operation, variables
	if decisions, ok := ctx.Value(policyDecisionsKey{}).(map[string]PolicyDecision); ok {
		lease.PolicySignature = policyRuntimeSignature(decisions)
	}
	ctx = withSubscriptionLease(ctx, lease)

	// 键在plan之后计算：codec入参已还原，等价请求（如shortId与数字ID）归并到同一feed
	scope := scopeValues(ctx)
	identity, _ := IdentityFromContext(ctx)
	route, _ := PinnedRoute(ctx)
	decisions, _ := ctx.Value(policyDecisionsKey{}).(map[string]PolicyDecision)
	policySignature := ""
	if len(decisions) > 0 {
		policySignature = policyRuntimeSignature(decisions)
	}
	key := subscriptionFeedKey(query, operationName, variables, scope, identity, route, policySignature)
	events := make(chan gqlReply, 1)

	my.feedMu.Lock()
	f := my.feeds[key]
	if f == nil {
		w, err := my.cdc.watch(subscriptionTableIdentities(my.metadata.SchemaName(), plan.tables))
		if err != nil {
			my.feedMu.Unlock()
			return nil, err
		}
		fctx, stop := context.WithCancel(WithScope(context.WithoutCancel(ctx), scope))
		f = &feed{key: key, subs: make(map[chan gqlReply]bool), stop: stop}
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
func (my *Engine) stream(ctx context.Context, plan *Plan, variables map[string]interface{}, w *watcher, f *feed) {
	defer my.cdc.unwatch(w)

	var last uint64
	lease, hasLease := subscriptionLeaseFromContext(ctx)
	var leaseTick <-chan time.Time
	var ticker *time.Ticker
	if hasLease {
		ticker = time.NewTicker(lease.Interval)
		leaseTick = ticker.C
		defer ticker.Stop()
	}
	digest := fnv.New64a() // stream单goroutine持有，tick内Reset复用免每次分配
	publish := func() {
		if reply, changed := my.tick(ctx, plan, variables, digest, &last); changed {
			my.feedMu.Lock()
			f.last, f.live = reply, true
			for ch := range f.subs {
				push(ch, reply)
			}
			my.feedMu.Unlock()
		}
	}
	publish()
	for {
		select {
		case <-leaseTick:
			if err := lease.Check(ctx); err != nil {
				my.expireFeed(f, err)
				return
			}
			if lease.PolicyProvider != nil && lease.Operation != nil {
				reauthed, err := my.authorizeProvider(ctx, lease.Operation, lease.Variables)
				if err != nil {
					my.expireFeed(f, err)
					return
				}
				decisions, _ := reauthed.Value(policyDecisionsKey{}).(map[string]PolicyDecision)
				if policyRuntimeSignature(decisions) != lease.PolicySignature {
					my.expireFeed(f, fmt.Errorf("subscription policy changed"))
					return
				}
			}
		case <-w.wake:
			publish()
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
func (my *Engine) leave(key string, f *feed, ch chan gqlReply) {
	my.feedMu.Lock()
	if !f.subs[ch] {
		my.feedMu.Unlock()
		return
	}
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

func (my *Engine) expireFeed(f *feed, err error) {
	my.feedMu.Lock()
	defer my.feedMu.Unlock()
	reply := gqlReply{Errors: GraphQLErrors(err)}
	for ch := range f.subs {
		push(ch, reply)
		close(ch)
	}
	f.subs = make(map[chan gqlReply]bool)
	f.stop()
	if my.feeds[f.key] == f {
		delete(my.feeds, f.key)
	}
}

func subscriptionFeedKey(query, operation string, variables, scope map[string]any, identity Identity, route TenantRoute, policy string) string {
	roles := identity.Roles()
	sort.Strings(roles)
	v, _ := json.Marshal(variables)
	s, _ := json.Marshal(scope)
	r, _ := json.Marshal(roles)
	return query + "\x00" + operation + "\x00" + string(v) + "\x00" + string(s) + "\x00" + identity.Tenant() + "\x00" + identity.Subject() + "\x00" + string(r) + "\x00" + fmt.Sprint(identity.PolicyRevision()) + "\x00" + route.CacheKey + "\x00" + policy
}

// tick 表变更唤醒后重查一次：结果指纹无变化时返回false（不推送）
func (my *Engine) tick(ctx context.Context, plan *Plan, variables map[string]interface{}, digest hash.Hash64, last *uint64) (gqlReply, bool) {
	var r gqlReply

	data, err := my.fetch(ctx, plan, variables)
	if err != nil {
		if ctx.Err() != nil {
			return r, false
		}
		r.Errors = GraphQLErrors(err)
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
		r.Errors = GraphQLErrors(err)
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
	CheckOrigin:     subscriptionOriginAllowedByHost,
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

func subscriptionOriginAllowedByHost(ctx *fasthttp.RequestCtx) bool {
	origin := strings.TrimSpace(string(ctx.Request.Header.Peek("Origin")))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, string(ctx.Host()))
}

// UpgradeContext 将完整的宿主认证上下文带入 WebSocket 会话。升级后
// fasthttp 请求上下文不可再作为认证状态来源，因此只从 ctx 派生取消信号，
// 不重建 context.Background()+scope。
func (my *Engine) UpgradeContext(rc *fasthttp.RequestCtx, ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Detach from fasthttp's request cancellation/value carrier before the
	// upgrader takes ownership of the request context. Values remain intact,
	// while the socket gets its own cancellation scope in serveSocket.
	ctx = context.WithoutCancel(ctx)
	return upgrader.Upgrade(rc, func(conn *websocket.Conn) {
		my.serveSocket(conn, ctx)
	})
}

// socketSession 单个WebSocket连接的订阅会话
type socketSession struct {
	mu   sync.Mutex
	conn *websocket.Conn
	subs map[string]context.CancelFunc
}

// maxActiveSubscriptions bounds per-connection fan-out and goroutine usage.
// It is intentionally a clear protocol-level constant until a public server
// configuration surface is introduced.
const maxActiveSubscriptions = 100

// write 串行化并发写
func (my *socketSession) write(message wsReply) error {
	my.mu.Lock()
	defer my.mu.Unlock()
	return my.conn.WriteJSON(message)
}

// wsInitTimeout connection_init握手时限，超时按协议关4408（测试可缩短）
var wsInitTimeout = 5 * time.Second

const wsReadLimit = 1 << 20

func subscriptionTableIdentity(schema, table string) string {
	if strings.Contains(table, ".") || schema == "" {
		return table
	}
	return schema + "." + table
}

func subscriptionTableIdentities(schema string, tables []string) []string {
	result := make([]string, 0, len(tables))
	for _, table := range tables {
		if table != "" {
			result = append(result, subscriptionTableIdentity(schema, table))
		}
	}
	return result
}

// closeSocket 按graphql-transport-ws协议码关闭连接（4401/4408/4429/4409...）
func closeSocket(conn *websocket.Conn, code int, reason string) {
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
	_ = conn.Close()
}

// serveSocket 连接读循环：init/ack、subscribe、complete、ping/pong。
// 协议状态机（graphql-transport-ws）：subscribe必须在init/ack之后（否则4401），
// init超时4408、重复init4429、重复订阅ID4409——鉴权握手不可绕过
// ctx 为HTTP升级阶段认证完成的完整上下文；只派生连接取消信号，保留
// Identity、PinnedRoute、scope 以及宿主挂载的 revocation/policy 状态。
func (my *Engine) serveSocket(conn *websocket.Conn, ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	conn.SetReadLimit(wsReadLimit)

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
			closeSocket(conn, 4400, "Invalid WebSocket message")
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
				session.mu.Lock()
				_, duplicate := session.subs[message.ID]
				atLimit := len(session.subs) >= maxActiveSubscriptions
				session.mu.Unlock()
				if atLimit && !duplicate {
					closeSocket(conn, 4400, "Too many active subscriptions")
				} else {
					closeSocket(conn, 4409, "Subscriber for "+message.ID+" already exists")
				}
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
	if _, exists := my.subs[id]; exists || len(my.subs) >= maxActiveSubscriptions {
		return false
	}
	my.subs[id] = nil
	return true
}

// startSubscription 启动单个订阅：消费事件通道并推送next帧
// 订阅ID已由调用方reserve预占，失败路径须释放占位
func (my *Engine) startSubscription(ctx context.Context, session *socketSession, message wsMessage) {
	release := func() {
		session.mu.Lock()
		delete(session.subs, message.ID)
		session.mu.Unlock()
	}
	var req gqlQuery
	if err := jsonNumeric.Unmarshal(message.Payload, &req); err != nil {
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
		_ = session.write(wsReply{ID: message.ID, Type: wsError, Payload: GraphQLErrors(err)})
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
