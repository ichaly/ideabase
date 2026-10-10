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

func TestReplaceDoesNotRequireDuplicateDeclaration(t *testing.T) {
	engine, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	err := engine.register(NewResolver("User", "name", "", func(context.Context, Source, struct{}) (string, error) { return "replaced", nil }))
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
	err := engine.register(NewResolver("User", "name", "", func(context.Context, Source, struct{}) (string, error) { return "ok", nil }, Requires("email")))
	require.NoError(t, err)
	node, _ := engine.metadata.GetNode("User")
	require.Equal(t, []string{"email"}, node.Fields["name"].Requires)
	err = engine.register(NewResolver("User", "name", "", func(context.Context, Source, struct{}) (string, error) { return "ok", nil }, Requires("missing")))
	require.Error(t, err)
	require.Equal(t, []string{"email"}, node.Fields["name"].Requires)
}
