package gql

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
)

func TestMaxDepthRejectsRootResolverBeforeInvocation(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, func(k *std.Konfig) {
		k.Set("schema.max-depth", 3)
	})
	defer cleanup()
	var calls atomic.Int32
	require.NoError(t, executor.Register(NewResolver[Root, struct{}, map[string]any]("Query", "deep", "", func(context.Context, Root, struct{}) (map[string]any, error) {
		calls.Add(1)
		return map[string]any{"child": map[string]any{"value": "ok"}}, nil
	}, Result("Deep"), Extra(`type Deep { child: Deep, value: String }`))))

	tooDeep := executor.run(context.Background(), `{ deep { child { child { value } } } }`, nil, "")
	require.NotEmpty(t, tooDeep.Errors)
	require.Zero(t, calls.Load(), "超深根Resolver不得调用")

	boundary := executor.run(context.Background(), `{ deep { child { value } } }`, nil, "")
	require.Empty(t, boundary.Errors)
	deep := boundary.Data["deep"].(map[string]any)
	require.Equal(t, "ok", deep["child"].(map[string]any)["value"])
	require.NotContains(t, deep, "value")

	sql := executor.run(context.Background(), `{ users { total } }`, nil, "")
	require.Empty(t, sql.Errors, "普通SQL查询不应被深度护栏误伤")
}

func TestMaxDepthRejectsPersistedRootResolverBeforePrewarm(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, func(k *std.Konfig) {
		k.Set("schema.max-depth", 2)
	})
	defer cleanup()
	var calls atomic.Int32
	require.NoError(t, executor.Register(NewResolver[Root, struct{}, map[string]any]("Query", "deep", "", func(context.Context, Root, struct{}) (map[string]any, error) {
		calls.Add(1)
		return map[string]any{"child": map[string]any{"value": "ok"}}, nil
	}, Result("Deep"), Extra(`type Deep { child: Deep, value: String }`))))

	err := executor.loadDocument(`query TooDeep { deep { child { value } } }`)
	require.Error(t, err)
	reply := executor.runOperation(context.Background(), "TooDeep", nil)
	require.NotEmpty(t, reply.Errors)
	require.Zero(t, calls.Load(), "超深持久化根Resolver不得预热或调用")
}

func TestPersistedDepthIgnoresConditionallySkippedFields(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, func(k *std.Konfig) {
		k.Set("schema.max-depth", 2)
	})
	defer cleanup()
	require.NoError(t, executor.Register(NewResolver[Root, struct{}, map[string]any]("Query", "deep", "", func(context.Context, Root, struct{}) (map[string]any, error) {
		return map[string]any{"child": map[string]any{"value": "ok"}}, nil
	}, Result("Deep"), Extra(`type Deep { child: Deep, value: String }`))))

	const query = `query ConditionalDepth($skip: Boolean!) { deep { child @skip(if: $skip) { value } } }`
	require.NoError(t, executor.loadDocument(query))
	reply := executor.runOperation(context.Background(), "ConditionalDepth", map[string]any{"skip": true})
	require.Empty(t, reply.Errors)
}
