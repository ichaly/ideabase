package gql

import (
	"context"
	"testing"
	"time"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
)

// TestSubscribe CDC订阅：首推当前结果，WAL变更触发推送，取消后通道关闭
func TestSubscribe(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	events, err := executor.Subscribe(ctx, `subscription {
		users { items { name } total }
	}`, nil, "")
	require.NoError(t, err, "建立订阅失败")

	next := func(hint string) gqlReply {
		select {
		case reply, ok := <-events:
			require.True(t, ok, "事件通道意外关闭: %s", hint)
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
	w := executor.Execute(ctx, `mutation { createUser(input: { name: "Eve", email: "e@x.com" }) { id } }`, nil, "")
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

// TestSubscribeScope 订阅按作用域隔离：订阅 ctx 的租户决定推送范围。
// 这是 WebSocket 升级丢 scope 修复的下游验证——证明订阅 fetch 确实用 scopeValues(ctx)
// 过滤；升级阶段把 HTTP ctx 的 scope 传到连接 ctx 那段为纯管道，由代码审查保证
func TestSubscribeScope(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	require.NoError(t, db.Exec(`ALTER TABLE users ADD COLUMN tenant_id INT NOT NULL DEFAULT 1`).Error)

	k, err := std.NewKonfig()
	require.NoError(t, err)
	k.Set("mode", "dev")
	k.Set("app.root", t.TempDir())
	k.Set("schema.schema", "public")
	k.Set("metadata.classes", map[string]*internal.ClassConfig{
		"User": {Table: "users", Scope: []internal.ScopeConfig{{Column: "tenant_id", Context: "tenant"}}},
	})
	meta, err := NewMetadata(k, db)
	require.NoError(t, err)
	compile, err := NewCompiler(meta, nil)
	require.NoError(t, err)
	executor, err := NewExecutor(db, NewRenderer(meta), meta, compile)
	require.NoError(t, err)

	// 租户2 预存数据——租户1 的订阅不应看到
	require.NoError(t, db.Exec(`INSERT INTO users (name, email, tenant_id) VALUES ('t2', 't2@x.com', 2)`).Error)

	ctx, cancel := context.WithTimeout(WithScope(context.Background(), map[string]any{"tenant": 1}), 15*time.Second)
	defer cancel()

	events, err := executor.Subscribe(ctx, `subscription { users { items { name } total } }`, nil, "")
	require.NoError(t, err, "建立订阅失败")

	next := func(hint string) gqlReply {
		select {
		case reply, ok := <-events:
			require.True(t, ok, "通道意外关闭: %s", hint)
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
	w := executor.Execute(ctx, `mutation { createUser(input: { name: "t1u", email: "t1u@x.com" }) { id } }`, nil, "")
	require.Empty(t, w.Errors, "创建失败: %v", w.Errors)
	reply = next("租户1新增")
	users := reply.Data["users"].(map[string]interface{})
	require.EqualValues(t, 1, users["total"])
	require.Equal(t, "t1u", users["items"].([]interface{})[0].(map[string]interface{})["name"])
}
