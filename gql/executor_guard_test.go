package gql

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 注册表(map)无锁,Register/rebuild与serving并发即数据竞争:
// 首次执行后冻结,运行期注册须明确报错而非静默竞争
func TestRegisterFrozenAfterServing(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, executor.Register(NewResolver("User", "early", "启动期注册",
		func(_ context.Context, _ Source, _ struct{}) (string, error) { return "ok", nil })))

	reply := executor.Execute(ctx, `query { users { total } }`, nil, "")
	require.Empty(t, reply.Errors)

	err := executor.Register(NewResolver("User", "late", "运行期注册",
		func(_ context.Context, _ Source, _ struct{}) (string, error) { return "", nil }))
	require.Error(t, err, "首次执行后注册必须报错")
	require.Contains(t, err.Error(), "冻结")
}

// schema.introspection=false 时自省查询必须拒绝(生产对外暴露的安全开关)
func TestIntrospectionDisabled(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, func(k *std.Konfig) { k.Set("schema.introspection", false) })
	defer cleanup()

	reply := executor.Execute(context.Background(), `query { __schema { queryType { name } } }`, nil, "")
	require.NotEmpty(t, reply.Errors, "自省关闭后__schema应拒绝")
	assert.Contains(t, reply.Errors[0].Message, "自省")

	// 常规查询不受影响
	reply = executor.Execute(context.Background(), `query { users { total } }`, nil, "")
	require.Empty(t, reply.Errors)
}

// schema.persisted-only=true 时HTTP边界只接受持久化操作(防任意查询打穿)
func TestPersistedOnlyHTTP(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, func(k *std.Konfig) { k.Set("schema.persisted-only", true) })
	defer cleanup()
	require.NoError(t, executor.loadDocument(`query UserTotal { users { total } }`))

	app := fiber.New()
	executor.Bind(app.Group(executor.Path()))
	post := func(body string) string {
		req := httptest.NewRequest("POST", executor.Path(), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		require.NoError(t, err)
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		return string(buf[:n])
	}

	// 原始查询文本拒绝
	raw := post(`{"query":"query { users { total } }"}`)
	assert.Contains(t, raw, "持久化", "原始查询应被拒绝: %s", raw)

	// 持久化操作名放行
	persisted := post(`{"operationName":"UserTotal"}`)
	assert.Contains(t, persisted, `"total"`, "持久化操作应放行: %s", persisted)
}
