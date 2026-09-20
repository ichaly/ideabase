package gql

import (
	"context"
	"database/sql"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
)

func TestHandlerRequiresProjectsDependenciesAndStripsThemFromResponse(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	var received atomic.Bool
	require.NoError(t, executor.register(NewResolver[Source, struct{}, string]("User", "displayName", "", func(_ context.Context, source Source, _ struct{}) (string, error) {
		received.Store(source["name"] == "Alice" && source["email"] == "alice@example.com")
		return source["name"].(string) + " <" + source["email"].(string) + ">", nil
	}, Result("String"), Requires("name", "email"))))

	seed := executor.run(context.Background(), `mutation { createUser(input: { name: "Alice", email: "alice@example.com" }) { id } }`, nil, "")
	require.Empty(t, seed.Errors)
	reply := executor.run(context.Background(), `{ users { items { displayName } } }`, nil, "")
	require.Empty(t, reply.Errors)
	require.True(t, received.Load(), "Requires字段应被内部投影给Resolver")
	item := reply.Data["users"].(map[string]any)["items"].([]any)[0].(map[string]any)
	require.Equal(t, "Alice <alice@example.com>", item["displayName"])
	_, hasName := item["name"]
	_, hasEmail := item["email"]
	require.False(t, hasName)
	require.False(t, hasEmail)
}

func TestHandlerRejectsUnknownRequiresForEntityAndRoot(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()

	err := executor.register(NewResolver[Source, struct{}, string]("User", "brokenDependency", "", func(context.Context, Source, struct{}) (string, error) {
		return "", nil
	}, Result("String"), Requires("doesNotExist")))
	require.Error(t, err)
	require.Contains(t, err.Error(), "依赖未知列字段")

	err = executor.register(NewResolver[Root[struct{}, string], struct{}, string]("Query", "brokenRootDependency", "", func(context.Context, Root[struct{}, string], struct{}) (string, error) {
		return "", nil
	}, Result("String"), Requires("id")))
	require.Error(t, err)
	require.Contains(t, err.Error(), "不能声明source依赖")
}

func TestHandlerTimeoutReportsStableTimeoutCode(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	require.NoError(t, executor.register(
		NewResolver[Root[struct{}, string], struct{}, string]("Query", "cooperativeTimeout", "", func(ctx context.Context, _ Root[struct{}, string], _ struct{}) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		}, Result("String"), Timeout(20*time.Millisecond)),
		NewResolver[Root[struct{}, string], struct{}, string]("Query", "ignoredTimeout", "", func(ctx context.Context, _ Root[struct{}, string], _ struct{}) (string, error) {
			<-ctx.Done()
			return "late", nil
		}, Result("String"), Timeout(20*time.Millisecond)),
	))

	for _, field := range []string{"cooperativeTimeout", "ignoredTimeout"} {
		reply := executor.run(context.Background(), "{ "+field+" }", nil, "")
		require.Len(t, reply.Errors, 1, field)
		require.Equal(t, "TIMEOUT", reply.Errors[0].Extensions["code"], field)
		require.Nil(t, reply.Data[field], field)
	}
}

func TestHandlerBatchMaxBatchRejectsBeforeInvocation(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	var calls atomic.Int32
	require.NoError(t, executor.register(NewBatch("Post", "limitedLabel", "", func(_ context.Context, sources []Source, _ struct{}) ([]string, error) {
		calls.Add(1)
		return make([]string, len(sources)), nil
	}, Result("String"), MaxBatch(1))))

	seed := executor.run(context.Background(), `mutation {
		createUser(input: { name: "Owner", email: "owner@example.com" }) { id }
	}`, nil, "")
	require.Empty(t, seed.Errors)
	owner := seed.Data["createUser"].(map[string]any)["id"]
	for _, title := range []string{"one", "two"} {
		created := executor.run(context.Background(), `mutation ($owner: ID!, $title: String!) {
			createPost(input: { userId: $owner, title: $title }) { id }
		}`, map[string]any{"owner": owner, "title": title}, "")
		require.Empty(t, created.Errors)
	}

	reply := executor.run(context.Background(), `{ posts { items { id limitedLabel } } }`, nil, "")
	require.NotEmpty(t, reply.Errors)
	require.Equal(t, int32(0), calls.Load())
	for _, err := range reply.Errors {
		require.Equal(t, "RESOLVER_ERROR", err.Extensions["code"])
	}
}

func TestHandlerRetrySafeRequiresIdempotencyKey(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	err := executor.register(NewResolver[Root[struct{}, string], struct{}, string]("Query", "unsafeRetry", "", func(context.Context, Root[struct{}, string], struct{}) (string, error) {
		return "", nil
	}, Result("String"), RetrySafe("")))
	require.Error(t, err)
	require.Contains(t, err.Error(), "必须提供幂等键")
}

func TestHandlerExecutionContextContainsTrustedRequestView(t *testing.T) {
	executor, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	identity, err := NewIdentity(IdentityInput{Tenant: "tenant-a", Subject: "user-a"})
	require.NoError(t, err)
	var view ExecutionContext
	var found bool
	var executionDB *std.Connection
	var executionTx *sql.Tx
	require.NoError(t, executor.register(NewResolver[Root[struct{}, string], struct{}, string]("Mutation", "inspectExecution", "", func(ctx context.Context, _ Root[struct{}, string], _ struct{}) (string, error) {
		view, found = ExecutionFrom(ctx)
		executionDB, executionTx = view.Connection(), view.Transaction()
		if !found || view.Identity().Subject() != "user-a" || view.SnapshotFingerprint() == "" || executionDB == nil || executionTx == nil {
			return "invalid", nil
		}
		require.Same(t, transactionFromContext(ctx), executionTx)
		return "ok", nil
	}, Result("String"))))

	reply := executor.run(WithIdentity(context.Background(), identity), `mutation { inspectExecution }`, nil, "")
	require.Empty(t, reply.Errors)
	require.Equal(t, "ok", reply.Data["inspectExecution"])
	require.True(t, found)
	require.Equal(t, identity, view.Identity())
	require.Equal(t, executor.mustSnapshotFingerprint(t), view.SnapshotFingerprint())
	require.Same(t, db, executionDB)
	require.Same(t, executionDB, view.Connection())
	require.Same(t, executionTx, view.Transaction())
}

func (my *Engine) mustSnapshotFingerprint(t *testing.T) string {
	t.Helper()
	snapshot, err := my.Snapshot()
	require.NoError(t, err)
	return snapshot.Fingerprint()
}
