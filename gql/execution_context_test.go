package gql

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"gorm.io/gorm"
)

type invalidationRecorder struct {
	mu     sync.Mutex
	tables []string
}

func (recorder *invalidationRecorder) invalidate(tables ...string) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.tables = append(recorder.tables, tables...)
}

func TestExecutionContextExposesTrustedRequestView(t *testing.T) {
	identity, err := NewIdentity(IdentityInput{Tenant: "tenant-a", Subject: "user-a"})
	require.NoError(t, err)
	deadline := time.Now().Add(time.Minute)
	base := &gorm.DB{Config: &gorm.Config{}, Statement: &gorm.Statement{Context: context.Background()}}
	selection := ast.SelectionSet{&ast.Field{Name: "users"}}
	ctx, cancel := context.WithDeadline(WithIdentity(context.Background(), identity), deadline)
	defer cancel()
	ctx = withExecutionContext(ctx, selection, "snapshot-1", base)

	execution, ok := ExecutionContextFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, identity, execution.Identity())
	require.Equal(t, "users", execution.Selection()[0].(*ast.Field).Name)
	require.Equal(t, "snapshot-1", execution.SnapshotFingerprint())
	gotDeadline, ok := execution.Deadline()
	require.True(t, ok)
	require.WithinDuration(t, deadline, gotDeadline, time.Millisecond)
	require.Same(t, ctx, execution.DB().Statement.Context)
	database, err := execution.RequireDB()
	require.NoError(t, err)
	require.Same(t, ctx, database.Statement.Context)
}

func TestExecutionContextSelectionIsDetachedFromCachedAST(t *testing.T) {
	original := ast.SelectionSet{&ast.Field{
		Name:         "user",
		Arguments:    ast.ArgumentList{&ast.Argument{Name: "id", Value: &ast.Value{Raw: "1"}}},
		SelectionSet: ast.SelectionSet{&ast.Field{Name: "name"}},
	}}
	view, ok := ExecutionContextFromContext(withExecutionContext(context.Background(), original, "fp", nil))
	require.True(t, ok)
	selected := view.Selection()[0].(*ast.Field)
	selected.Name = "mutated"
	selected.Arguments[0].Value.Raw = "2"
	selected.SelectionSet[0].(*ast.Field).Name = "mutatedChild"

	field := original[0].(*ast.Field)
	require.Equal(t, "user", field.Name)
	require.Equal(t, "1", field.Arguments[0].Value.Raw)
	require.Equal(t, "name", field.SelectionSet[0].(*ast.Field).Name)
}

func TestExecutionContextDBPrefersTransaction(t *testing.T) {
	base, tx := &gorm.DB{}, &gorm.DB{}
	ctx := withExecutionContext(context.Background(), nil, "", base)
	ctx = context.WithValue(ctx, transactionKey{}, tx)

	execution, ok := ExecutionContextFromContext(ctx)
	require.True(t, ok)
	require.Same(t, tx, execution.DB())
	database, err := execution.RequireDB()
	require.NoError(t, err)
	require.Same(t, tx, database)
	require.Same(t, tx, transactionFromContext(ctx))
}

func TestExecutionContextDoesNotInventIdentityOrDatabase(t *testing.T) {
	ctx := withExecutionContext(context.Background(), nil, "", nil)
	execution, ok := ExecutionContextFromContext(ctx)
	require.True(t, ok)
	_, hasIdentity := execution.IdentityValue()
	require.False(t, hasIdentity)
	require.Nil(t, execution.DB())
	_, err := execution.RequireDB()
	require.ErrorIs(t, err, ErrDatabaseUnavailable)

	_, ok = ExecutionContextFromContext(context.Background())
	require.False(t, ok)
}

func TestExecutionContextExposesGovernanceViewDefensively(t *testing.T) {
	decision := PolicyDecision{Allow: true, Fields: map[FieldUse][]string{FieldUseSelect: {"id"}}}
	ctx := context.WithValue(context.Background(), policyDecisionsKey{}, map[string]PolicyDecision{"Query.user": decision})
	ctx = WithScope(ctx, map[string]any{"tenant": "t1"})
	ctx = withExecutionContext(ctx, nil, "fp", nil, Capability{Coordinate: "Query.user", Actions: []string{"select"}})
	execution, ok := ExecutionContextFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "Query.user", mustCoordinate(t, execution))
	got, ok := execution.PolicyDecision()
	require.True(t, ok)
	got.Fields[FieldUseSelect][0] = "changed"
	fresh, _ := execution.PolicyDecision()
	require.Equal(t, "id", fresh.Fields[FieldUseSelect][0])
	scope := execution.Scope()
	scope["tenant"] = "changed"
	require.Equal(t, "t1", execution.Scope()["tenant"])
}

func TestEngineExecutionContextBindsSelectedCapability(t *testing.T) {
	capability := Capability{Coordinate: "Query.user", SemanticKind: SemanticProjection}
	engine := &Engine{snapshot: &Snapshot{fingerprint: "fp", index: map[string]Capability{"Query.user": capability}}}
	view, ok := ExecutionContextFromContext(engine.executionContextFor(context.Background(), nil, "Query.user"))
	require.True(t, ok)
	selected, ok := view.Capability()
	require.True(t, ok)
	require.Equal(t, capability.Coordinate, selected.Coordinate)
}

func mustCoordinate(t *testing.T, execution ExecutionContext) string {
	t.Helper()
	coordinate, ok := execution.Coordinate()
	require.True(t, ok)
	return coordinate
}

func TestInvalidateRequiresEngineTransactionAndDeduplicates(t *testing.T) {
	require.Error(t, (ExecutionContext{}).Invalidate("users"))
	ctx := withInvalidationState(context.Background())
	ctx = context.WithValue(ctx, engineMutationTransactionKey{}, true)
	ctx = withExecutionContext(ctx, nil, "fp", nil)
	execution, _ := ExecutionContextFromContext(ctx)
	require.NoError(t, execution.Invalidate("users", "users", "posts"))
	require.Equal(t, []string{"posts", "users"}, invalidationTables(ctx))
}

func TestInvalidationPublishesOnlyAfterOwnedTransactionCommit(t *testing.T) {
	_, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	recorder := &invalidationRecorder{}
	ctx := withInvalidationState(context.Background())

	err, rolledBack := runRootTransaction(ctx, db, recorder, func(tx *gorm.DB) error {
		execution := context.WithValue(ctx, transactionKey{}, tx)
		execution = context.WithValue(execution, engineMutationTransactionKey{}, true)
		view, _ := ExecutionContextFromContext(withExecutionContext(execution, nil, "fp", db))
		return view.Invalidate("users", "users", "posts")
	})
	require.NoError(t, err)
	require.False(t, rolledBack)
	require.Equal(t, []string{"posts", "users"}, recorder.tables)

	recorder.tables = nil
	ctx = withInvalidationState(context.Background())
	err, rolledBack = runRootTransaction(ctx, db, recorder, func(tx *gorm.DB) error {
		execution := context.WithValue(ctx, transactionKey{}, tx)
		execution = context.WithValue(execution, engineMutationTransactionKey{}, true)
		view, _ := ExecutionContextFromContext(withExecutionContext(execution, nil, "fp", db))
		require.NoError(t, view.Invalidate("users"))
		return errors.New("rollback")
	})
	require.Error(t, err)
	require.True(t, rolledBack)
	require.Empty(t, recorder.tables)
}
