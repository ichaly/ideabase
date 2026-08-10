package gql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Deterministic allocation budgets are required gates; wall-clock benchmarks
// remain relative base/HEAD diagnostics because absolute ns/op is runner noise.
func TestGAAllocationBudgets(t *testing.T) {
	t.Run("reply", func(t *testing.T) {
		reply := gqlReply{raw: sampleRows(50)}
		require.LessOrEqual(t, testing.AllocsPerRun(100, func() {
			if _, err := reply.MarshalJSON(); err != nil {
				panic(err)
			}
		}), 1.0)
	})
	t.Run("resolver hosts", func(t *testing.T) {
		root := sampleTree(8)
		require.LessOrEqual(t, testing.AllocsPerRun(100, func() { _ = hosts(root, []string{"items", "children"}) }), 11.0)
	})
	t.Run("warm gateway", func(t *testing.T) {
		child := newGatewayEndpoint("crm", gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
			return []byte(`{"data":{"user":{"id":"1"}}}`), nil
		})
		gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
		require.NoError(t, err)
		query := `{ crm { user { id } } }`
		_, err = gateway.Execute(context.Background(), query, nil)
		require.NoError(t, err)
		require.LessOrEqual(t, testing.AllocsPerRun(100, func() {
			if _, executeErr := gateway.Execute(context.Background(), query, nil); executeErr != nil {
				panic(executeErr)
			}
		}), 80.0)
	})
}
