package gql

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSubscribe 轮询订阅：首推当前结果，数据变化后推送新结果，取消后通道关闭
func TestSubscribe(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	executor.SetInterval(100 * time.Millisecond)

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
