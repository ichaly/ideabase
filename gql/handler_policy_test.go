package gql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandlerRequiresParticipateInPolicyFieldUses(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	called := false
	require.NoError(t, executor.Register(NewResolver(
		"User", "displayName", "", func(_ context.Context, source Source, _ struct{}) (string, error) {
			called = true
			return source["name"].(string), nil
		}, Requires("name"),
	)))
	require.NoError(t, executor.Register(OperationPolicyProvider(func(_ context.Context, request PolicyRequest) (PolicyDecision, error) {
		if request.Coordinate == "Query.users" {
			require.Equal(t, []string{"displayName"}, request.Fields[FieldUseJoin])
			require.Equal(t, []string{"name"}, request.Fields[FieldUseInternalDependency])
			return PolicyDecision{Allow: true, Fields: map[FieldUse][]string{
				FieldUseJoin: {"displayName"},
				// Deliberately withhold the internal dependency.
			}}, nil
		}
		return PolicyDecision{Allow: true, Fields: request.Fields}, nil
	})))
	identity, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "u"})
	require.NoError(t, err)
	body, err := executor.Execute(WithIdentity(context.Background(), identity), `{ users { items { displayName } } }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(body), "field-use=internal-dependency field=name")
	require.NotContains(t, string(body), `"data"`)
	require.False(t, called, "policy rejection must happen before SQL and Handler invocation")
}
