package gql

import (
	"context"
	"testing"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/require"
)

func TestProtocolPolicyProviderCanonicalWriteFieldsForInsertAction(t *testing.T) {
	provider := NewProtocolPolicyProvider(map[string]map[string]*protocol.Permission{
		"items": {"writer": {Insert: []string{"insert_only"}, Update: []string{"update_only"}}},
	})

	decision, err := provider.Decide(context.Background(), canonicalPolicyRequest(t, []string{protocol.OP_INSERT}, "writer"))
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"insert_only"}, decision.Fields[FieldUseWrite])
	require.NotContains(t, decision.Fields[FieldUseWrite], "update_only")
}

func TestProtocolPolicyProviderCanonicalWriteFieldsForUpdateAction(t *testing.T) {
	provider := NewProtocolPolicyProvider(map[string]map[string]*protocol.Permission{
		"items": {"writer": {Insert: []string{"insert_only"}, Update: []string{"update_only"}}},
	})

	decision, err := provider.Decide(context.Background(), canonicalPolicyRequest(t, []string{protocol.OP_UPDATE}, "writer"))
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"update_only"}, decision.Fields[FieldUseWrite])
	require.NotContains(t, decision.Fields[FieldUseWrite], "insert_only")
}

func TestProtocolPolicyProviderCanonicalUpsertWriteFieldsFollowActionCombination(t *testing.T) {
	provider := NewProtocolPolicyProvider(map[string]map[string]*protocol.Permission{
		"items": {"writer": {Insert: []string{"insert_only", "shared"}, Update: []string{"update_only", "shared"}}},
	})

	tests := []struct {
		name    string
		actions []string
		fields  []string
	}{
		{name: "insert branch", actions: []string{protocol.OP_INSERT}, fields: []string{"insert_only", "shared"}},
		{name: "update branch", actions: []string{protocol.OP_UPDATE}, fields: []string{"update_only", "shared"}},
		{name: "upsert branches", actions: []string{protocol.OP_INSERT, protocol.OP_UPDATE}, fields: []string{"shared"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, err := provider.Decide(context.Background(), canonicalPolicyRequest(t, test.actions, "writer"))
			require.NoError(t, err)
			require.ElementsMatch(t, test.fields, decision.Fields[FieldUseWrite])
		})
	}
}

func TestProtocolPolicyProviderRoleBranchesOnlyIncludeRolesAuthorizedForAction(t *testing.T) {
	provider := NewProtocolPolicyProvider(map[string]map[string]*protocol.Permission{
		"items": {
			"insertor": {
				Insert: []string{"insert_only"},
				Filter: []protocol.Predicate{{Column: "insert_scope", Operator: "eq", Context: "subject"}},
				Check:  []protocol.Predicate{{Column: "insert_check", Operator: "eq", Context: "subject"}},
			},
			"updater": {
				Update: []string{"update_only"},
				Filter: []protocol.Predicate{{Column: "update_scope", Operator: "eq", Context: "subject"}},
				Check:  []protocol.Predicate{{Column: "update_check", Operator: "eq", Context: "subject"}},
			},
		},
	})

	for _, test := range []struct {
		name           string
		action         string
		expectedRole   string
		unexpectedRole string
	}{
		{name: "insert", action: protocol.OP_INSERT, expectedRole: "insertor", unexpectedRole: "updater"},
		{name: "update", action: protocol.OP_UPDATE, expectedRole: "updater", unexpectedRole: "insertor"},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision, err := provider.Decide(context.Background(), canonicalPolicyRequest(t, []string{test.action}, "insertor", "updater"))
			require.NoError(t, err)
			require.Len(t, decision.RoleBranches, 1)
			require.Equal(t, test.expectedRole, decision.RoleBranches[0].Role)
			require.NotEqual(t, test.unexpectedRole, decision.RoleBranches[0].Role)
		})
	}
}

func canonicalPolicyRequest(t *testing.T, actions []string, roles ...string) PolicyRequest {
	t.Helper()
	identity, err := NewIdentity(IdentityInput{Tenant: "tenant", Subject: "subject", Roles: roles})
	require.NoError(t, err)
	return PolicyRequest{
		Identity:   identity,
		Route:      TenantRoute{Tenant: "tenant"},
		Coordinate: "Mutation.items",
		Resource:   "items",
		Actions:    actions,
		FieldUses:  []FieldUse{FieldUseWrite},
	}
}
