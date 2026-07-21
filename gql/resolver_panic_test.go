package gql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestResolverPanicIsolation 用户实现panic必须转为该字段/请求的错误:
// errgroup不recover,goroutine里的panic会打崩整个服务进程
func TestResolverPanicIsolation(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, executor.Register(
		NewResolver("User", "boom", "panic字段",
			func(_ context.Context, _ Source, _ struct{}) (string, error) { panic("resolver炸了") }),
		NewBatch("User", "batchBoom", "批量panic字段",
			func(_ context.Context, sources []Source, _ struct{}) ([]string, error) { panic("batch炸了") }),
		NewRemote("User", "extra", "远程panic", "id",
			func(_ context.Context, keys []any) (map[any]Profile, error) { panic("remote炸了") }),
	))

	reply := executor.run(ctx, `mutation { createUser(input: { name: "P", email: "p@x.com" }) { id } }`, nil, "")
	require.Empty(t, reply.Errors, "种数据失败: %v", reply.Errors)

	// 普通resolver(有界并发goroutine)panic → 请求级错误
	reply = executor.run(ctx, `query { users { items { id boom } } }`, nil, "")
	require.NotEmpty(t, reply.Errors, "resolver panic应转为错误")
	require.Contains(t, reply.Errors[0].Message, "panic")

	// 批量resolver panic → 请求级错误
	reply = executor.run(ctx, `query { users { items { id batchBoom } } }`, nil, "")
	require.NotEmpty(t, reply.Errors, "batch resolver panic应转为错误")
	require.Contains(t, reply.Errors[0].Message, "panic")

	// remote panic → 容错语义:字段置null,警告随errors返回,数据不丢
	reply = executor.run(ctx, `query { users { items { name extra { level } } } }`, nil, "")
	require.NotEmpty(t, reply.Errors, "remote panic应作为警告返回")
	items := reply.Data["users"].(map[string]interface{})["items"].([]interface{})
	require.NotEmpty(t, items, "remote容错不中断主查询")
	require.Nil(t, items[0].(map[string]interface{})["extra"], "panic的远程字段应置null")
}
