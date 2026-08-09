package gql

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Executor owns one immutable catalog. Mutating the builder metadata after
// construction must not change planning, rendering, policy, or fingerprints.
func TestExecutorFreezesMetadataCatalog(t *testing.T) {
	meta := createMockMetadata(t)
	meta.legacyTrusted = true
	compiler, err := NewCompiler(meta, nil)
	require.NoError(t, err)
	executor, err := NewExecutor(nil, NewRenderer(meta), meta, compiler)
	require.NoError(t, err)

	meta.Nodes["User"].Table = "forged_users"
	delete(meta.Nodes["User"].Fields, "name")
	delete(meta.operations, "Query.users")

	require.Equal(t, "users", executor.metadata.Nodes["User"].Table)
	require.Contains(t, executor.metadata.Nodes["User"].Fields, "name")
	require.Contains(t, executor.metadata.operations, "Query.users")
}
