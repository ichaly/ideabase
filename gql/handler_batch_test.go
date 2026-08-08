package gql

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBatchHandlerDeduplicatesEquivalentAliasesPerRequest(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	var calls atomic.Int32
	require.NoError(t, executor.Register(NewBatch(
		"User", "batchLabel", "",
		func(_ context.Context, sources []Source, _ struct{}) ([]string, error) {
			calls.Add(1)
			values := make([]string, len(sources))
			for i := range values {
				values[i] = "resolved"
			}
			return values, nil
		}, Result("String"), Requires("id"),
	)))
	created := executor.run(context.Background(), `mutation {
		createUser(input: {name: "Batch", email: "batch-dedup@example.com"}) { id }
	}`, nil, "")
	require.Empty(t, created.Errors)
	reply := executor.run(context.Background(), `{ users { items { a: batchLabel b: batchLabel } } }`, nil, "")
	require.Empty(t, reply.Errors)
	require.Equal(t, int32(1), calls.Load())
	item := reply.Data["users"].(map[string]any)["items"].([]any)[0].(map[string]any)
	require.Equal(t, "resolved", item["a"])
	require.Equal(t, "resolved", item["b"])
	require.NotContains(t, item, "id")
}
