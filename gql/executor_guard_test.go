package gql

import (
	"context"
	"strings"
	"testing"

	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponseLimitStopsResolverReplyAtExecutionBoundary(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	require.NoError(t, executor.register(NewResolver[Root, struct{}, string]("Query", "largeReply", "", func(context.Context, Root, struct{}) (string, error) {
		return strings.Repeat("x", 1024), nil
	})))
	body, err := executor.Execute(WithResponseLimit(context.Background(), 128), `{ largeReply }`, nil)
	require.Nil(t, body)
	require.ErrorContains(t, err, "response size limit")
}

// schema.introspection=false 时自省查询必须拒绝(生产对外暴露的安全开关)
func TestIntrospectionDisabled(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, func(k *std.Konfig) { k.Set("schema.introspection", false) })
	defer cleanup()

	reply := executor.run(context.Background(), `query { __schema { queryType { name } } }`, nil, "")
	require.NotEmpty(t, reply.Errors, "自省关闭后__schema应拒绝")
	assert.Contains(t, reply.Errors[0].Message, "自省")

	// 常规查询不受影响
	reply = executor.run(context.Background(), `query { users { total } }`, nil, "")
	require.Empty(t, reply.Errors)
}

// schema.persisted-only=true 时HTTP边界只接受持久化操作(防任意查询打穿)
func TestPersistedOnlyHTTP(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, func(k *std.Konfig) { k.Set("schema.persisted-only", true) })
	defer cleanup()
	require.NoError(t, executor.loadDocument(`query UserTotal { users { total } }`))

	post := func(body string) string {
		var req struct {
			Query         string `json:"query"`
			OperationName string `json:"operationName"`
		}
		require.NoError(t, jsonNumeric.Unmarshal([]byte(body), &req))
		out, err := executor.ExecuteOperation(context.Background(), req.Query, nil, req.OperationName)
		require.NoError(t, err)
		return string(out)
	}

	// 原始查询文本拒绝
	raw := post(`{"query":"query { users { total } }"}`)
	assert.Contains(t, raw, "持久化", "原始查询应被拒绝: %s", raw)

	// 持久化操作名放行
	persisted := post(`{"operationName":"UserTotal"}`)
	assert.Contains(t, persisted, `"total"`, "持久化操作应放行: %s", persisted)
}

func TestExecuteOperationIsThePersistedOperationBoundary(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, func(k *std.Konfig) { k.Set("schema.persisted-only", true) })
	defer cleanup()
	require.NoError(t, executor.loadDocument(`query UserTotal { users { total } }`))
	body, err := executor.ExecuteOperation(context.Background(), "", nil, "UserTotal")
	require.NoError(t, err)
	assert.Contains(t, string(body), `"total"`)
	body, err = executor.ExecuteOperation(context.Background(), `query { users { total } }`, nil, "")
	require.NoError(t, err)
	assert.Contains(t, string(body), "持久化")
}

func TestPersistedOnlyRejectsDirectExecute(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, func(k *std.Konfig) { k.Set("schema.persisted-only", true) })
	defer cleanup()
	body, err := executor.Execute(context.Background(), `query { users { total } }`, nil)
	require.NoError(t, err)
	assert.Contains(t, string(body), "持久化")
}

func TestPersistedOnlyRejectsDirectSubscriptionDocument(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, func(k *std.Konfig) { k.Set("schema.persisted-only", true) })
	defer cleanup()
	_, err := executor.Subscribe(context.Background(), `subscription { users { total } }`, nil, "")
	require.ErrorContains(t, err, "持久化")
}

func TestPersistedOnlyAcceptsGatewayAdmittedDocument(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, func(k *std.Konfig) { k.Set("schema.persisted-only", true) })
	defer cleanup()
	gateway, err := NewGateway("root", Namespace{Name: "db", EndpointID: "db", Endpoint: executor})
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), `{ db { users { total } } }`, nil)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"total":0`)
}
