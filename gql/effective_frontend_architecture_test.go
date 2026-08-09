package gql

import (
	"context"
	"strings"
	"testing"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/require"
)

// Validation and introspection must consume the same effective schema. A field
// hidden from the identity-specific schema is an unknown GraphQL coordinate,
// not a later policy execution error.
func TestExecutionValidatesAgainstEffectiveSchema(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	installTestPolicy(t, executor, OperationPolicyProvider(func(_ context.Context, request PolicyRequest) (PolicyDecision, error) {
		fields := request.Fields
		if request.Resource == "User" {
			fields = make(map[FieldUse][]string, len(request.FieldUses))
			for _, use := range request.FieldUses {
				fields[use] = []string{"id"}
			}
		}
		return PolicyDecision{Allow: true, Fields: fields, PolicyRevision: 7}, nil
	}))
	identity, err := NewIdentity(IdentityInput{Tenant: "tenant", Subject: "reader", PolicyRevision: 7})
	require.NoError(t, err)

	body, err := executor.Execute(WithIdentity(context.Background(), identity), `{ users { items { name } } }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(body), `Cannot query field \"name\" on type \"User\"`)
	require.NotContains(t, string(body), "field-use")
}

func TestEffectiveSchemaSeparatesCreateUpdateAndUpsertInputs(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	installTestPolicy(t, executor, NewProtocolPolicyProvider(map[string]map[string]*protocol.Permission{
		"User": {"writer": {Select: []string{"id", "name", "email"}, Insert: []string{"name"}, Update: []string{"name", "email"}}},
	}))
	identity, err := NewIdentity(IdentityInput{Tenant: "tenant", Subject: "writer", Roles: []string{"writer"}, PolicyRevision: 9})
	require.NoError(t, err)

	artifact, err := executor.EffectiveSchema(WithIdentity(context.Background(), identity))
	require.NoError(t, err)
	require.Contains(t, schemaBlock(artifact.Schema, "input UserCreateInput"), "name:")
	require.NotContains(t, schemaBlock(artifact.Schema, "input UserCreateInput"), "email:")
	require.Contains(t, schemaBlock(artifact.Schema, "input UserUpdateInput"), "email:")
	require.Contains(t, schemaBlock(artifact.Schema, "input UserUpsertRowInput"), "name:")
	require.NotContains(t, schemaBlock(artifact.Schema, "input UserUpsertRowInput"), "email:")
}

func schemaBlock(schema, declaration string) string {
	start := strings.Index(schema, declaration+" {")
	if start < 0 {
		return ""
	}
	end := strings.Index(schema[start:], "\n}")
	if end < 0 {
		return schema[start:]
	}
	return schema[start : start+end+2]
}
