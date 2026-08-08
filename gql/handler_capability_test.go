package gql

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHandlerOperationalContractIsPartOfSnapshot(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	require.NoError(t, executor.Register(NewBatch(
		"User", "contractField", "",
		func(context.Context, []Source, struct{}) ([]string, error) { return nil, nil },
		Result("String"), Requires("id"), Timeout(time.Second), MaxBatch(17), RetrySafe("request-id"),
	)))
	snapshot, err := executor.Snapshot()
	require.NoError(t, err)
	capability, ok := snapshot.Capability("User.contractField")
	require.True(t, ok)
	require.Equal(t, HandlerResolver, capability.Handler)
	require.Equal(t, []string{"id"}, capability.Requires)
	require.Equal(t, time.Second, capability.Timeout)
	require.Equal(t, 17, capability.MaxBatch)
	require.True(t, capability.RetrySafe)
	require.Equal(t, "request-id", capability.IdempotencyKey)
}
