package gql

import (
	"context"
	"github.com/stretchr/testify/require"
	"sync/atomic"
	"testing"
	"time"
)

func TestKeyedLoadingUsesResolverContract(t *testing.T) {
	loader := NewLoader("User", "profile", "", "id", func(context.Context, []any) (map[any]Profile, error) { return nil, nil })
	_, ok := any(loader).(Resolver)
	require.True(t, ok, "keyed loading must use the resolver contract")
}

func TestMiddlewarePreservesBatchExecution(t *testing.T) {
	calls := 0
	resolver := NewBatch("User", "labels", "", func(_ context.Context, sources []Source, _ struct{}) ([]string, error) {
		calls++
		out := make([]string, len(sources))
		for i, s := range sources {
			out[i] = s["name"].(string)
		}
		return out, nil
	})
	wrap := NewResolverMiddleware(func(ctx context.Context, sources []Source, input struct{}, next NextResolver[[]Source, struct{}, []string]) ([]string, error) {
		values, err := next(ctx, sources, input)
		for i := range values {
			values[i] += "!"
		}
		return values, err
	})
	wrapped := wrap(resolver)
	many, ok := wrapped.(BatchResolver)
	require.True(t, ok, "middleware must preserve batching")
	values, err := many.ResolveBatch(context.Background(), []Source{{"name": "A"}, {"name": "B"}}, nil)
	require.NoError(t, err)
	require.Equal(t, []any{"A!", "B!"}, values)
	require.Equal(t, 1, calls)
}

func TestReplaceDoesNotRequireDuplicateDeclaration(t *testing.T) {
	engine, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	err := engine.replace(NewResolver("User", "name", "", func(context.Context, Source, struct{}) (string, error) { return "replaced", nil }))
	require.NoError(t, err)
}

func TestIndependentLoadersExecuteConcurrently(t *testing.T) {
	engine, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	testExec(t, db, `INSERT INTO users(name,email) VALUES ('parallel','parallel@example.com')`)
	var entered atomic.Int32
	ready := make(chan struct{})
	fetch := func(ctx context.Context, keys []any) (map[any]Profile, error) {
		if entered.Add(1) == 2 {
			close(ready)
		}
		select {
		case <-ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return map[any]Profile{keys[0]: {Level: "ok"}}, nil
	}
	require.NoError(t, engine.register(NewLoader("User", "first", "", "id", fetch), NewLoader("User", "second", "", "id", fetch)))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reply := engine.run(ctx, `{users {items {first {level} second {level}}}}`, nil, "")
	require.Empty(t, reply.Errors)
	require.EqualValues(t, 2, entered.Load())
}

func TestReplaceUpdatesDependencies(t *testing.T) {
	engine, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	err := engine.replace(NewResolver("User", "name", "", func(context.Context, Source, struct{}) (string, error) { return "ok", nil }, Requires("email")))
	require.NoError(t, err)
	node, _ := engine.metadata.GetNode("User")
	require.Equal(t, []string{"email"}, node.Fields["name"].Requires)
	err = engine.replace(NewResolver("User", "name", "", func(context.Context, Source, struct{}) (string, error) { return "ok", nil }, Requires("missing")))
	require.Error(t, err)
	require.Equal(t, []string{"email"}, node.Fields["name"].Requires)
}

type externalBatch struct{ BatchResolver }

func TestMiddlewarePreservesExternalBatch(t *testing.T) {
	calls := 0
	resolver := externalBatch{NewBatch("User", "label", "", func(_ context.Context, sources []Source, _ struct{}) ([]any, error) {
		calls++
		return []any{"A", "B"}, nil
	}).(BatchResolver)}
	wrap := NewResolverMiddleware(func(ctx context.Context, sources []Source, input struct{}, next NextResolver[[]Source, struct{}, []any]) ([]any, error) {
		return next(ctx, sources, input)
	})
	values, err := wrap(resolver).(BatchResolver).ResolveBatch(context.Background(), []Source{{}, {}}, nil)
	require.NoError(t, err)
	require.Equal(t, []any{"A", "B"}, values)
	require.Equal(t, 1, calls)
}
