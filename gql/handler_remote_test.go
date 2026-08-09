package gql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestRemoteHandlerReceivesRequestedSelection(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	var selection ast.SelectionSet
	require.NoError(t, executor.register(NewRemote(
		"User", "selectedProfile", "", "id",
		func(ctx context.Context, keys []any) (map[any]Profile, error) {
			view, ok := ExecutionContextFromContext(ctx)
			if ok {
				selection = view.Selection()
			}
			out := make(map[any]Profile, len(keys))
			for _, key := range keys {
				out[key] = Profile{Level: "selected"}
			}
			return out, nil
		},
	)))
	created := executor.run(context.Background(), `mutation {
		createUser(input: {name: "Remote", email: "remote-selection@example.com"}) { id }
	}`, nil, "")
	require.Empty(t, created.Errors)
	reply := executor.run(context.Background(), `{ users { items { selectedProfile { level } } } }`, nil, "")
	require.Empty(t, reply.Errors)
	require.Len(t, selection, 1)
	require.Equal(t, "level", selection[0].(*ast.Field).Name)
}
