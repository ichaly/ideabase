package gql

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"testing"
	"time"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
)

func decodeSubscriptionReply(t *testing.T, reply gqlReply) gqlReply {
	t.Helper()
	if reply.encoded == nil {
		return reply
	}
	var decoded gqlReply
	require.NoError(t, jsonNumeric.Unmarshal(reply.encoded, &decoded))
	normalizeNumbers(decoded.Data)
	return decoded
}

func TestSubscriptionAdmissionLimitsFeedsAndSubscribers(t *testing.T) {
	engine := &Engine{feeds: make(map[string]*feed)}
	for i := range maxTenantSubscriptionFeeds {
		key := fmt.Sprint(i)
		engine.feeds[key] = &feed{key: key, tenant: "tenant", subs: make(map[chan gqlReply]bool)}
	}
	err := engine.checkSubscriptionLimit("tenant", nil)
	require.Equal(t, "LIMIT_EXCEEDED", GraphQLErrors(err)[0].Extensions["code"])
	require.NoError(t, engine.checkSubscriptionLimit("other", nil))

	shared := &feed{subs: make(map[chan gqlReply]bool)}
	for range maxSubscribersPerFeed {
		shared.subs[make(chan gqlReply)] = true
	}
	err = engine.checkSubscriptionLimit("tenant", shared)
	require.Equal(t, "LIMIT_EXCEEDED", GraphQLErrors(err)[0].Extensions["code"])
}

func TestAcquireRefreshDoesNotHoldTenantWhileGlobalIsBusy(t *testing.T) {
	engine := &Engine{refreshSem: make(chan struct{}, 1)}
	for i := range engine.tenantRefresh {
		engine.tenantRefresh[i] = make(chan struct{}, 1)
	}
	engine.refreshSem <- struct{}{}
	identity, err := NewIdentity(IdentityInput{Tenant: "tenant-b", Subject: "user"})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(WithIdentity(context.Background(), identity), 10*time.Millisecond)
	defer cancel()
	release, acquired := engine.acquireRefresh(ctx)
	require.False(t, acquired)
	release()
	select {
	case engine.tenantRefresh[tenantRefreshSlot("tenant-b")] <- struct{}{}:
	default:
		t.Fatal("tenant slot remained held after global wait timed out")
	}
}

func TestSubscriptionReplyLimitReturnsBoundedTerminalError(t *testing.T) {
	ctx := WithResponseLimit(context.Background(), 256)
	body, err := marshalSubscriptionReply(ctx, gqlReply{Data: map[string]any{"value": strings.Repeat("x", 1024)}})
	require.NoError(t, err)
	require.LessOrEqual(t, len(body), 256)
	require.Contains(t, string(body), "LIMIT_EXCEEDED")
	require.NotContains(t, string(body), strings.Repeat("x", 64))

	_, err = marshalSubscriptionReply(WithResponseLimit(context.Background(), 8), gqlReply{Data: map[string]any{"value": strings.Repeat("x", 1024)}})
	require.ErrorIs(t, err, errResponseLimit)
}

func TestSubscriptionFeedReplyIsBoundedBeforeCaching(t *testing.T) {
	reply := boundSubscriptionReply(WithResponseLimit(context.Background(), 128), gqlReply{Data: map[string]any{"value": strings.Repeat("x", 4096)}})
	require.Nil(t, reply.Data)
	require.LessOrEqual(t, len(reply.encoded), 128)
	require.Contains(t, string(reply.encoded), "LIMIT_EXCEEDED")
}

func TestSubscriptionFeedKeyHasFixedLengthAndIsolatesIdentity(t *testing.T) {
	first, err := NewIdentity(IdentityInput{Tenant: "tenant-a", Subject: "user-a", Roles: []string{"reader"}})
	require.NoError(t, err)
	second, err := NewIdentity(IdentityInput{Tenant: "tenant-a", Subject: "user-b", Roles: []string{"reader"}})
	require.NoError(t, err)
	small := subscriptionFeedKey("query", "Op", map[string]any{"n": 1}, nil, first, TenantRoute{}, "policy")
	large := subscriptionFeedKey(strings.Repeat("query", 10000), "Op", map[string]any{"n": strings.Repeat("x", 10000)}, map[string]any{"tenant": strings.Repeat("t", 10000)}, first, TenantRoute{}, "policy")
	require.Equal(t, len(small), len(large))
	require.NotEqual(t, small, subscriptionFeedKey("query", "Op", map[string]any{"n": 1}, nil, second, TenantRoute{}, "policy"))
}

func TestSubscriptionIdenticalFailurePublishesOnceAndKeepsRetrying(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	plan := &Plan{SQL: `SELECT '{'::text`}
	digest := fnv.New64a()
	var last uint64
	for attempt := 0; attempt < 2; attempt++ {
		reply, changed := executor.tick(context.Background(), plan, nil, digest, &last)
		require.Equal(t, attempt == 0, changed)
		require.NotEmpty(t, reply.Errors)
		require.NotZero(t, last, "failed result must be retained for duplicate suppression")
	}
}

func TestSubscriptionFingerprintSeparatesDataAndErrors(t *testing.T) {
	digest := fnv.New64a()
	var last uint64
	require.True(t, publishFingerprint(digest, &last, 'd', []byte("same")))
	require.True(t, publishFingerprint(digest, &last, 'e', []byte("same")))
	require.False(t, publishFingerprint(digest, &last, 'e', []byte("same")))
}

func TestSubscriptionRetriesUntilDatabaseRecovers(t *testing.T) {
	executor, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	if executor.cdc != nil {
		executor.cdc.close()
	}
	executor.cdc = runtimeTestNotifier{}
	plan, err := executor.plan(`subscription { users { total } }`, "", nil)
	require.NoError(t, err)
	plan.SQL = strings.ReplaceAll(plan.SQL, `"public"."users"`, `"public"."retry_users"`)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &watcher{wake: make(chan struct{}, 1)}
	events := make(chan gqlReply, 2)
	f := &feed{key: "retry", subs: map[chan gqlReply]bool{events: true}, stop: cancel, done: ctx.Done()}
	executor.streamWG.Add(1)
	go executor.stream(ctx, plan, nil, w, f)

	select {
	case reply := <-events:
		reply = decodeSubscriptionReply(t, reply)
		require.NotEmpty(t, reply.Errors)
	case <-time.After(3 * time.Second):
		t.Fatal("missing initial database error")
	}
	testExec(t, db, `CREATE VIEW retry_users AS SELECT * FROM users`)
	select {
	case reply := <-events:
		reply = decodeSubscriptionReply(t, reply)
		require.Empty(t, reply.Errors)
		require.EqualValues(t, 0, reply.Data["users"].(map[string]any)["total"])
	case <-time.After(3 * time.Second):
		t.Fatal("subscription did not recover through retry")
	}
}

// TestSubscribe CDC订阅：首推当前结果，WAL变更触发推送，取消后通道关闭
func TestSubscribe(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	events, err := executor.subscribe(ctx, `subscription {
		users { items { name } total }
	}`, nil, "")
	require.NoError(t, err, "建立订阅失败")

	next := func(hint string) gqlReply {
		select {
		case reply, ok := <-events:
			require.True(t, ok, "事件通道意外关闭: %s", hint)
			reply = decodeSubscriptionReply(t, reply)
			require.Empty(t, reply.Errors, "%s: %v", hint, reply.Errors)
			return reply
		case <-time.After(5 * time.Second):
			t.Fatalf("等待订阅事件超时: %s", hint)
			return gqlReply{}
		}
	}

	// 首次推送当前结果（空集）
	reply := next("首次推送")
	require.EqualValues(t, 0, reply.Data["users"].(map[string]interface{})["total"])

	// 数据变化触发推送
	w := executor.run(ctx, `mutation { createUser(input: { name: "Eve", email: "e@x.com" }) { id } }`, nil, "")
	require.Empty(t, w.Errors, "创建用户失败: %v", w.Errors)

	reply = next("变化推送")
	users := reply.Data["users"].(map[string]interface{})
	require.EqualValues(t, 1, users["total"])
	require.Equal(t, "Eve", users["items"].([]interface{})[0].(map[string]interface{})["name"])

	// 取消订阅后通道关闭
	cancel()
	select {
	case _, ok := <-events:
		if ok { // 可能还有缓冲事件，再读一次
			_, ok = <-events
			require.False(t, ok, "取消后通道应关闭")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消后通道未关闭")
	}
}

// TestSubscribeShared 同构订阅共享一份重查流：两个订阅只建一个feed且各自收到推送；
// 先退一个feed仍存续，全部退出后feed摘除
func TestSubscribeShared(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()

	query := `subscription { users { items { name } total } }`
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	e1, err := executor.subscribe(ctx1, query, nil, "")
	require.NoError(t, err)
	e2, err := executor.subscribe(ctx2, query, nil, "")
	require.NoError(t, err)

	executor.feedMu.Lock()
	require.Len(t, executor.feeds, 1, "同构订阅应共享同一feed")
	executor.feedMu.Unlock()

	first := func(events <-chan gqlReply, hint string) {
		select {
		case reply, ok := <-events:
			require.True(t, ok, "通道意外关闭: %s", hint)
			reply = decodeSubscriptionReply(t, reply)
			require.Empty(t, reply.Errors, "%s: %v", hint, reply.Errors)
		case <-time.After(5 * time.Second):
			t.Fatalf("等待首次推送超时: %s", hint)
		}
	}
	first(e1, "订阅1")
	first(e2, "订阅2应获最近结果补发")

	// 退出一个：其通道关闭（leave在close前完成），feed因另一订阅存续
	cancel1()
	select {
	case _, ok := <-e1:
		if ok {
			_, ok = <-e1
			require.False(t, ok, "取消后通道应关闭")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消后通道未关闭")
	}
	executor.feedMu.Lock()
	require.Len(t, executor.feeds, 1, "仍有订阅者时feed应存续")
	executor.feedMu.Unlock()

	// 全部退出：feed摘除
	cancel2()
	require.Eventually(t, func() bool {
		executor.feedMu.Lock()
		defer executor.feedMu.Unlock()
		return len(executor.feeds) == 0
	}, 3*time.Second, 50*time.Millisecond, "全部退出后feed应摘除")
}

// TestSubscribeScope 订阅按作用域隔离：订阅 ctx 的租户决定推送范围。
// 这是 WebSocket 升级丢 scope 修复的下游验证——证明订阅 fetch 确实用 scopeValues(ctx)
// 过滤；升级阶段把 HTTP ctx 的 scope 传到连接 ctx 那段为纯管道，由代码审查保证
func TestSubscribeScope(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	testExec(t, db, `ALTER TABLE users ADD COLUMN tenant_id INT NOT NULL DEFAULT 1`)

	k, err := std.NewKonfig()
	require.NoError(t, err)
	k.Set("mode", "dev")
	k.Set("app.root", t.TempDir())
	k.Set("schema.schema", "public")
	k.Set("metadata.classes", map[string]*internal.ClassConfig{
		"User": {Table: "users", Scope: []protocol.ScopeRule{{Column: "tenant_id", Context: "tenant"}}},
	})
	meta, err := NewMetadata(k, db)
	require.NoError(t, err)
	compile, err := NewCompiler(meta, nil)
	require.NoError(t, err)
	executor, err := newEngineForTest(db, NewRenderer(meta), meta, compile)
	require.NoError(t, err)

	// 租户2 预存数据——租户1 的订阅不应看到
	testExec(t, db, `INSERT INTO users (name, email, tenant_id) VALUES ('t2', 't2@x.com', 2)`)

	ctx, cancel := context.WithTimeout(WithScope(context.Background(), map[string]any{"tenant": 1}), 15*time.Second)
	defer cancel()

	events, err := executor.subscribe(ctx, `subscription { users { items { name } total } }`, nil, "")
	require.NoError(t, err, "建立订阅失败")

	next := func(hint string) gqlReply {
		select {
		case reply, ok := <-events:
			require.True(t, ok, "通道意外关闭: %s", hint)
			reply = decodeSubscriptionReply(t, reply)
			require.Empty(t, reply.Errors, "%s: %v", hint, reply.Errors)
			return reply
		case <-time.After(5 * time.Second):
			t.Fatalf("等待订阅事件超时: %s", hint)
			return gqlReply{}
		}
	}

	// 首次推送：租户1 视角，看不到租户2 的预存数据
	reply := next("首次推送")
	require.EqualValues(t, 0, reply.Data["users"].(map[string]interface{})["total"], "订阅按作用域隔离，租户1 看不到租户2")

	// 租户1 新增（作用域写入也是租户1）→ 推送包含它
	w := executor.run(ctx, `mutation { createUser(input: { name: "t1u", email: "t1u@x.com" }) { id } }`, nil, "")
	require.Empty(t, w.Errors, "创建失败: %v", w.Errors)
	reply = next("租户1新增")
	users := reply.Data["users"].(map[string]interface{})
	require.EqualValues(t, 1, users["total"])
	require.Equal(t, "t1u", users["items"].([]interface{})[0].(map[string]interface{})["name"])
}

func TestSubscriptionRetryDelayIsBoundedAndExponential(t *testing.T) {
	a := subscriptionRetryDelay(0)
	b := subscriptionRetryDelay(1)
	require.Greater(t, b, a)
	require.LessOrEqual(t, subscriptionRetryDelay(20), 30*time.Second)
}
