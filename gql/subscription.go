package gql

import (
	"context"
	"crypto/sha256"
	"fmt"
	"hash"
	"hash/fnv"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
)

// feed 共享订阅流：同构订阅（查询+变量+作用域）共用一次重查循环，结果扇出。
// 热点订阅的重查次数与订阅者数解耦：表变更时每份feed只打一条SQL
type feed struct {
	key    string
	tenant string
	subs   map[chan gqlReply]bool
	last   gqlReply // 最近一次推送，后来的订阅者立即补发（免重查）
	live   bool     // last是否已有效
	stop   context.CancelFunc
	done   <-chan struct{}
}

const cdcReconnectGrace = 30 * time.Second
const subscriptionRefreshTimeout = 5 * time.Second
const subscriptionCacheResponseLimit int64 = 1 << 20

const (
	maxSubscriptionFeeds       = 1024
	maxTenantSubscriptionFeeds = 128
	maxSubscribersPerFeed      = 256
)

func (my *Engine) checkSubscriptionLimit(tenant string, existing *feed) error {
	if existing != nil {
		if len(existing.subs) >= maxSubscribersPerFeed {
			return &publicFailure{"subscription subscriber limit exceeded", "LIMIT_EXCEEDED"}
		}
		return nil
	}
	if len(my.feeds) >= maxSubscriptionFeeds {
		return &publicFailure{"subscription feed limit exceeded", "LIMIT_EXCEEDED"}
	}
	count := 0
	for _, current := range my.feeds {
		if current.tenant == tenant {
			count++
		}
	}
	if count >= maxTenantSubscriptionFeeds {
		return &publicFailure{"tenant subscription feed limit exceeded", "LIMIT_EXCEEDED"}
	}
	return nil
}

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

func subscriptionRetryDelay(attempt int) time.Duration {
	if attempt > 10 {
		attempt = 10
	}
	d := 100 * time.Millisecond * time.Duration(1<<attempt)
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d/2 + time.Duration(rand.Int63n(int64(d/2)+1))
}

func (my *Engine) acquireRefresh(ctx context.Context) (func(), bool) {
	tenant := ""
	if identity, ok := IdentityFromContext(ctx); ok {
		tenant = identity.Tenant()
	} else if route, ok := PinnedRoute(ctx); ok && route.Tenant != "" {
		tenant = route.Tenant
	} else if scope := scopeValues(ctx); scope != nil {
		tenant = fmt.Sprint(scope["tenant"])
	}
	sem := my.tenantRefresh[tenantRefreshSlot(tenant)]
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return func() {}, false
	}
	select {
	case my.refreshSem <- struct{}{}:
		return func() { <-my.refreshSem; <-sem }, true
	case <-ctx.Done():
		<-sem
		return func() {}, false
	}
}

func tenantRefreshSlot(tenant string) uint64 {
	const (
		offset = uint64(14695981039346656037)
		prime  = uint64(1099511628211)
	)
	hash := offset
	for i := 0; i < len(tenant); i++ {
		hash = (hash ^ uint64(tenant[i])) * prime
	}
	return hash % 64
}

// subscribe 订阅查询：基于WAL逻辑复制(CDC)的变更推送
// 订阅涉及的表发生变更时重执行查询，结果指纹变化才推送最新状态
// （慢消费者不阻塞其他订阅者，未消费的旧结果被最新结果顶替）；
// 首次立即推送当前结果；ctx取消后通道关闭
// 依赖数据库 wal_level=logical 与连接账号的REPLICATION权限
func (my *Engine) subscribe(ctx context.Context, query string, variables map[string]interface{}, operationName string) (<-chan gqlReply, error) {
	if my == nil || my.closed.Load() {
		return nil, fmt.Errorf("gql engine is closed")
	}
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
	key := subscriptionFeedKey(query, operationName, variables, scope, identity, route, lease.PolicySignature)
	events := make(chan gqlReply, 1)

	my.feedMu.Lock()
	if my.closed.Load() {
		my.feedMu.Unlock()
		return nil, fmt.Errorf("gql engine is closed")
	}
	f := my.feeds[key]
	if err := my.checkSubscriptionLimit(identity.Tenant(), f); err != nil {
		my.feedMu.Unlock()
		return nil, err
	}
	if f == nil {
		w, err := my.cdc.watch(subscriptionTableIdentities(my.metadata.SchemaName(), plan.tables))
		if err != nil {
			my.feedMu.Unlock()
			return nil, err
		}
		fctx, stop := context.WithCancel(WithScope(context.WithoutCancel(ctx), scope))
		f = &feed{key: key, tenant: identity.Tenant(), subs: make(map[chan gqlReply]bool), stop: stop, done: fctx.Done()}
		my.feeds[key] = f
		my.streamWG.Add(1)
		go my.stream(fctx, plan, variables, w, f)
	}
	f.subs[events] = true
	if f.live {
		events <- f.last // 补发最近结果：通道新建且容量1，必不阻塞
	}
	my.feedMu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
			my.leave(key, f, events)
		case <-f.done:
		}
	}()
	return events, nil
}

// stream 共享流事件循环：首查推送，之后表变更唤醒重查，变化结果扇出给全部订阅者
func (my *Engine) stream(ctx context.Context, plan *Plan, variables map[string]interface{}, w *watcher, f *feed) {
	defer my.streamWG.Done()
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
	attempt := 0
	var retry *time.Timer
	var retryC <-chan time.Time
	defer func() {
		if retry != nil {
			retry.Stop()
		}
	}()
	stopRetry := func() {
		attempt = 0
		retryC = nil
		if retry != nil && !retry.Stop() {
			select {
			case <-retry.C:
			default:
			}
		}
	}
	scheduleRetry := func() {
		attempt++
		delay := subscriptionRetryDelay(attempt)
		if retry == nil {
			retry = time.NewTimer(delay)
		} else {
			if !retry.Stop() {
				select {
				case <-retry.C:
				default:
				}
			}
			retry.Reset(delay)
		}
		retryC = retry.C
	}
	publish := func() bool {
		if w.status != nil {
			if connected, _ := w.status(); !connected {
				if w.outage == nil || w.outage() < cdcReconnectGrace {
					scheduleRetry()
					return true
				}
				my.expireFeed(f, &publicFailure{"CDC replication connection lost", "SUBSCRIPTION_SOURCE_UNAVAILABLE"})
				return false
			}
		}
		refreshCtx, cancel := context.WithTimeout(ctx, subscriptionRefreshTimeout)
		release, acquired := my.acquireRefresh(refreshCtx)
		defer cancel()
		if !acquired {
			scheduleRetry()
			return true
		}
		reply, changed := my.tickWithinDeadline(refreshCtx, plan, variables, &last, release, func() error {
			if hasLease {
				return lease.Check(refreshCtx)
			}
			return nil
		})
		failed := reply.Errors != nil
		reply = boundSubscriptionReply(ctx, reply)
		if failed {
			scheduleRetry()
		} else {
			stopRetry()
		}
		if !changed {
			return true
		}
		my.feedMu.Lock()
		f.last, f.live = reply, true
		for ch := range f.subs {
			push(ch, reply)
		}
		my.feedMu.Unlock()
		return true
	}
	if !publish() {
		return
	}
	for {
		select {
		case <-leaseTick:
			if err := my.checkLeaseWithinDeadline(ctx, lease); err != nil {
				my.expireFeed(f, err)
				return
			}
		case <-w.wake:
			if !publish() {
				return
			}
		case <-retryC:
			if !publish() {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func boundSubscriptionReply(ctx context.Context, reply gqlReply) gqlReply {
	limit := ResponseLimit(ctx)
	if limit <= 0 || limit > subscriptionCacheResponseLimit {
		limit = subscriptionCacheResponseLimit
	}
	body, err := marshalReply(WithResponseLimit(context.Background(), limit), reply)
	if err != nil {
		limited := gqlReply{Errors: GraphQLErrors(errResponseLimit)}
		limited.encoded, _ = marshalReply(context.Background(), limited)
		return limited
	}
	return gqlReply{encoded: body}
}

func (my *Engine) tickWithinDeadline(ctx context.Context, plan *Plan, variables map[string]interface{}, last *uint64, release func(), check func() error) (gqlReply, bool) {
	type result struct {
		reply   gqlReply
		changed bool
		last    uint64
	}
	resultCh := make(chan result, 1)
	localLast := *last
	go func() {
		defer release()
		if err := check(); err != nil {
			resultCh <- result{reply: gqlReply{Errors: GraphQLErrors(err)}, changed: true, last: localLast}
			return
		}
		digest := fnv.New64a()
		reply, changed := my.tick(ctx, plan, variables, digest, &localLast)
		resultCh <- result{reply: reply, changed: changed, last: localLast}
	}()
	select {
	case result := <-resultCh:
		*last = result.last
		return result.reply, result.changed
	case <-ctx.Done():
		return gqlReply{Errors: GraphQLErrors(ctx.Err())}, true
	}
}

func (my *Engine) checkLeaseWithinDeadline(ctx context.Context, lease subscriptionLease) error {
	deadline, cancel := context.WithTimeout(ctx, subscriptionRefreshTimeout)
	defer cancel()
	release, acquired := my.acquireRefresh(deadline)
	if !acquired {
		return deadline.Err()
	}
	result := make(chan error, 1)
	go func() {
		defer release()
		if err := lease.Check(deadline); err != nil {
			result <- err
			return
		}
		if lease.PolicyProvider == nil || lease.Operation == nil {
			result <- nil
			return
		}
		reauthed, err := my.authorizeProvider(deadline, lease.Operation, lease.Variables)
		if err == nil {
			decisions, _ := reauthed.Value(policyDecisionsKey{}).(map[string]PolicyDecision)
			if policyRuntimeSignature(decisions) != lease.PolicySignature {
				err = fmt.Errorf("subscription policy changed")
			}
		}
		result <- err
	}()
	select {
	case err := <-result:
		return err
	case <-deadline.Done():
		return deadline.Err()
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
	canonical, _ := json.Marshal(struct {
		Query, Operation       string
		Variables, Scope       map[string]any
		Tenant, Subject, Actor string
		Roles                  []string
		Policy, Credential     uint64
		Route, PolicySignature string
	}{query, operation, variables, scope, identity.Tenant(), identity.Subject(), identity.Actor(), roles, identity.PolicyRevision(), identity.CredentialEpoch(), route.CacheKey, policy})
	digest := sha256.Sum256(canonical)
	return fmt.Sprintf("subscription:v1:%x", digest[:])
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
		return r, publishReplyFingerprint(digest, last, 'e', &r)
	}

	// 订阅始终解包为Data供程序化消费（变更推送频率低，非热路径），
	// 懒解包残留的原始字节段就地物化，维持Data全map契约
	result, warnings, err := my.unpack(ctx, plan, data, variables)
	if err != nil {
		r.Errors = GraphQLErrors(err)
		return r, publishReplyFingerprint(digest, last, 'e', &r)
	}
	materialize(result)
	r.Errors, r.Data = warnings, result
	return r, publishReplyFingerprint(digest, last, 'd', &r)
}

func publishReplyFingerprint(digest hash.Hash64, last *uint64, kind byte, reply *gqlReply) bool {
	payload, err := reply.MarshalJSON()
	if err != nil {
		payload = []byte(err.Error())
	} else {
		reply.encoded = payload
	}
	return publishFingerprint(digest, last, kind, payload)
}

// publishFingerprint keeps retry state separate from successful data. Errors
// are retried in the background but identical failures are emitted once.
func publishFingerprint(digest hash.Hash64, last *uint64, kind byte, payload []byte) bool {
	digest.Reset()
	_, _ = digest.Write([]byte{kind, 0})
	_, _ = digest.Write(payload)
	sum := digest.Sum64()
	if sum == *last {
		return false
	}
	*last = sum
	return true
}
