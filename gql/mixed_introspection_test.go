package gql

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
)

func TestIntrospectionCanMixWithDataField(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()

	reply := executor.run(context.Background(), `query {
		userType: __type(name: "User") { name kind }
		users(limit: 1) { total }
	}`, nil, "")
	require.Empty(t, reply.Errors)
	require.Equal(t, "User", reply.Data["userType"].(map[string]any)["name"])
	require.Contains(t, reply.Data, "users")
}

func TestIntrospectionCanMixWithRootResolver(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	require.NoError(t, executor.Register(NewResolver[Root, struct{}, string]("Query", "health", "", func(context.Context, Root, struct{}) (string, error) {
		return "ok", nil
	})))

	reply := executor.run(context.Background(), `{ health __schema { queryType { name } } }`, nil, "")
	require.Empty(t, reply.Errors)
	require.Equal(t, "ok", reply.Data["health"])
	require.Equal(t, "Query", reply.Data["__schema"].(map[string]any)["queryType"].(map[string]any)["name"])
}

func TestMixedIntrospectionUsesCurrentVariablesOnCacheHit(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	query := `query($name: String!) { users(limit: 1) { total } selected: __type(name: $name) { name } }`
	for _, name := range []string{"User", "Query"} {
		reply := executor.run(context.Background(), query, map[string]any{"name": name}, "")
		require.Empty(t, reply.Errors)
		require.Equal(t, name, reply.Data["selected"].(map[string]any)["name"])
	}
}

func TestConditionalMixedIntrospectionPartitionsPlanShape(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	query := `query($show: Boolean!) { users(limit: 1) { total } __schema @include(if: $show) { queryType { name } } }`
	hidden := executor.run(context.Background(), query, map[string]any{"show": false}, "")
	require.Empty(t, hidden.Errors)
	require.NotContains(t, hidden.Data, "__schema")
	shown := executor.run(context.Background(), query, map[string]any{"show": true}, "")
	require.Empty(t, shown.Errors)
	require.Contains(t, shown.Data, "__schema")
}

func TestPersistedQueryCanMixIntrospectionAndData(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	require.NoError(t, executor.loadDocument(`query Mixed { users(limit: 1) { total } __type(name: "User") { name } }`))
	reply := executor.runOperation(context.Background(), "Mixed", nil)
	require.Empty(t, reply.Errors)
	require.Equal(t, "User", reply.Data["__type"].(map[string]any)["name"])
}

func TestDisabledIntrospectionRejectsMixedQueryBeforeDataExecution(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, func(k *std.Konfig) { k.Set("schema.introspection", false) })
	defer cleanup()
	var calls atomic.Int32
	require.NoError(t, executor.Register(NewResolver[Root, struct{}, string]("Query", "health", "", func(context.Context, Root, struct{}) (string, error) {
		calls.Add(1)
		return "ok", nil
	})))
	reply := executor.run(context.Background(), `query { health __schema { queryType { name } } }`, nil, "")
	require.NotEmpty(t, reply.Errors)
	require.Contains(t, reply.Errors[0].Message, "自省")
	require.Zero(t, calls.Load())
}
