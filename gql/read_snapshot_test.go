package gql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadSnapshotSharesRepeatableReadTransactionWithResolvers(t *testing.T) {
	executor, _, cleanup := setupRootResolverExecutor(t)
	defer cleanup()
	require.NoError(t, executor.register(NewResolver[Root, struct{}, bool]("Query", "hasSnapshotTx", "", func(ctx context.Context, _ Root, _ struct{}) (bool, error) {
		execution, ok := ExecutionFrom(ctx)
		require.True(t, ok)
		require.Same(t, executor.database, execution.Connection())
		tx := execution.Transaction()
		require.NotNil(t, tx)
		require.Same(t, transactionFromContext(ctx), tx)
		var isolation string
		require.NoError(t, tx.QueryRowContext(ctx, `SHOW transaction_isolation`).Scan(&isolation))
		require.Equal(t, "repeatable read", isolation)
		return true, nil
	})))
	reply := executor.run(WithReadSnapshot(context.Background()), `{ users { total } hasSnapshotTx }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	require.Equal(t, true, reply.Data["hasSnapshotTx"])
}

func TestReadSnapshotRejectsMutationBeforeSQL(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	reply := executor.run(WithReadSnapshot(context.Background()), `mutation { createUser(input: {name:"x", email:"x@test"}) { id } }`, nil, "")
	require.NotEmpty(t, reply.Errors)
	require.Equal(t, "READ_SNAPSHOT_MUTATION", reply.Errors[0].Extensions["code"])
}
