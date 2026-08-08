package gql

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"gorm.io/gorm"
)

func TestExecutionContextExposesTrustedRequestView(t *testing.T) {
	identity, err := NewIdentity(IdentityInput{Tenant: "tenant-a", Subject: "user-a"})
	require.NoError(t, err)
	deadline := time.Now().Add(time.Minute)
	base := &gorm.DB{Config: &gorm.Config{}, Statement: &gorm.Statement{Context: context.Background()}}
	selection := ast.SelectionSet{&ast.Field{Name: "users"}}
	ctx, cancel := context.WithDeadline(WithIdentity(context.Background(), identity), deadline)
	defer cancel()
	ctx = WithExecutionContext(ctx, selection, "snapshot-1", base)

	execution, ok := ExecutionContextFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, identity, execution.Identity())
	require.Equal(t, "users", execution.Selection()[0].(*ast.Field).Name)
	require.Equal(t, "snapshot-1", execution.SnapshotFingerprint())
	gotDeadline, ok := execution.Deadline()
	require.True(t, ok)
	require.WithinDuration(t, deadline, gotDeadline, time.Millisecond)
	require.Same(t, ctx, execution.DB().Statement.Context)
	require.Same(t, ctx, DB(ctx).Statement.Context)
}

func TestExecutionContextDBPrefersTransaction(t *testing.T) {
	base, tx := &gorm.DB{}, &gorm.DB{}
	ctx := WithExecutionContext(context.Background(), nil, "", base)
	ctx = context.WithValue(ctx, transactionKey{}, tx)

	execution, ok := ExecutionContextFromContext(ctx)
	require.True(t, ok)
	require.Same(t, tx, execution.DB())
	require.Same(t, tx, DB(ctx))
	require.Same(t, tx, Tx(ctx))
}

func TestExecutionContextDoesNotInventIdentityOrDatabase(t *testing.T) {
	ctx := WithExecutionContext(context.Background(), nil, "", nil)
	execution, ok := ExecutionContextFromContext(ctx)
	require.True(t, ok)
	_, hasIdentity := execution.IdentityValue()
	require.False(t, hasIdentity)
	require.Nil(t, execution.DB())
	require.Nil(t, DB(ctx))
	_, err := RequireDB(ctx)
	require.ErrorIs(t, err, ErrDatabaseUnavailable)

	_, ok = ExecutionContextFromContext(context.Background())
	require.False(t, ok)
}
