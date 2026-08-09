package gql

import (
	"context"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPolicyDecisionValidatesAndSeparatesShapeFromValues(t *testing.T) {
	id, err := NewIdentity(IdentityInput{Tenant: "t1", Subject: "u1", PolicyRevision: 4})
	require.NoError(t, err)
	route := TenantRoute{Mode: TenantModeSharedTable, Tenant: "t1", DataSource: "db", Revision: 8, CacheKey: "route"}
	request := PolicyRequest{Identity: id, Route: route, Coordinate: "Query.users", Resource: "users", Actions: []string{"select"}, FieldUses: []FieldUse{FieldUseSelect, FieldUseFilter}, Values: map[string]any{"tenant": "t1"}}
	decision := PolicyDecision{Allow: true, PolicyRevision: 4, RowPredicates: []TypedPredicate{{Column: "tenant_id", Operator: "eq", Context: "tenant"}}, Limit: 50, Cost: 3}
	got, err := NormalizeDecision(request, decision)
	require.NoError(t, err)
	require.NotEmpty(t, got.PermissionSignature)
	require.NotEmpty(t, got.ShapeSignature)
	changed := decision
	changed.RowPredicates = []TypedPredicate{{Column: "tenant_id", Operator: "eq", Context: "tenant", Value: "different"}}
	changed, err = NormalizeDecision(request, changed)
	require.NoError(t, err)
	require.Equal(t, got.ShapeSignature, changed.ShapeSignature)
	require.Equal(t, got.PermissionSignature, changed.PermissionSignature)
	got.RowPredicates[0].Column = "mutated"
	require.Equal(t, "tenant_id", decision.RowPredicates[0].Column)
}

func TestBuiltinPolicyProviderAdaptsProtocolPermission(t *testing.T) {
	id, err := NewIdentity(IdentityInput{Tenant: "t1", Subject: "u", Roles: []string{"reader"}, PolicyRevision: 11})
	require.NoError(t, err)
	provider := NewProtocolPolicyProvider(map[string]map[string]*protocol.Permission{
		"users": {"reader": {Select: []string{"id", "name"}, Filter: []protocol.Predicate{{Column: "tenant_id", Operator: "eq", Context: "tenant"}}}},
	})
	decision, err := provider.Decide(context.Background(), PolicyRequest{Identity: id, Route: TenantRoute{Tenant: "t1", Revision: 2}, Coordinate: "Query.users", Resource: "users", Actions: []string{"select"}, FieldUses: []FieldUse{FieldUseSelect}})
	require.NoError(t, err)
	require.True(t, decision.Allow)
	require.Equal(t, []string{"id", "name"}, decision.Fields[FieldUseSelect])
	require.Len(t, decision.RoleBranches, 1)
	require.Len(t, decision.RoleBranches[0].RowPredicates, 1)
	require.Equal(t, uint64(11), decision.PolicyRevision)
	denied, err := provider.Decide(context.Background(), PolicyRequest{Identity: mustPolicyIdentity(t, "other"), Route: TenantRoute{Tenant: "t1"}, Coordinate: "Query.users", Resource: "users", Actions: []string{"select"}})
	require.NoError(t, err)
	require.False(t, denied.Allow)
}

func TestBuiltinPolicyProviderKeepsStablePerRoleBranches(t *testing.T) {
	permissions := map[string]map[string]*protocol.Permission{"users": {
		"owner": {
			Select: []string{"name", "id"}, Insert: []string{"name"},
			Filter: []protocol.Predicate{{Column: "tenant_id", Operator: "eq", Context: "tenant"}, {Column: "owner_id", Operator: "eq", Context: "subject"}},
			Check:  []protocol.Predicate{{Column: "owner_id", Operator: "eq", Context: "subject"}},
		},
		"auditor": {Select: []string{"id"}, Update: []string{"name"}, Filter: []protocol.Predicate{{Column: "region", Operator: "eq", Context: "region"}}},
	}}
	provider := NewProtocolPolicyProvider(permissions)
	decide := func(roles []string) PolicyDecision {
		identity, err := NewIdentity(IdentityInput{Tenant: "t1", Subject: "u", Roles: roles})
		require.NoError(t, err)
		decision, err := provider.Decide(context.Background(), PolicyRequest{
			Identity: identity, Route: TenantRoute{Tenant: "t1"}, Coordinate: "Query.users", Resource: "users", Actions: []string{"select"}, FieldUses: []FieldUse{FieldUseSelect, FieldUseWrite},
		})
		require.NoError(t, err)
		return decision
	}
	first := decide([]string{"owner", "auditor"})
	second := decide([]string{"auditor", "owner"})
	require.Equal(t, first.PermissionSignature, second.PermissionSignature)
	require.Equal(t, first.ShapeSignature, second.ShapeSignature)
	require.Len(t, first.RoleBranches, 2)
	require.Len(t, first.RoleBranches[1].RowPredicates, 2, "同一角色的filter保持AND分组")
	require.Len(t, first.RoleBranches[1].Check, 1)
	require.Empty(t, first.Fields[FieldUseWrite], "select决策不得携带未请求的写权限")

	permissions["users"]["owner"].Select[0] = "mutated"
	require.NotContains(t, decide([]string{"owner"}).Fields[FieldUseSelect], "mutated")
}

func TestNormalizeDecisionRejectsInvalidRoleBranch(t *testing.T) {
	request := PolicyRequest{Identity: mustPolicyIdentity(t, "u"), Route: TenantRoute{Tenant: "t1"}, Coordinate: "Query.users", Resource: "users"}
	request.Actions = []string{"select"}
	_, err := NormalizeDecision(request, PolicyDecision{Allow: true, RoleBranches: []RolePolicyBranch{{Role: "reader", RowPredicates: []TypedPredicate{{Column: "", Operator: "eq"}}}}})
	require.Error(t, err)
}

func TestPolicyDecisionValidatesEveryCatalogReference(t *testing.T) {
	class := &protocol.Class{Name: "User", Fields: map[string]*protocol.Field{}}
	class.AddField(&protocol.Field{Name: "tenantId", Column: "tenant id"})
	valid := PolicyDecision{
		Fields:        map[FieldUse][]string{FieldUseFilter: {"tenantId"}},
		RowPredicates: []TypedPredicate{{Column: "tenant id", Operator: "eq", Context: "tenant"}},
	}
	require.NoError(t, ValidateDecisionCatalog(class, valid))
	invalid := valid
	invalid.Fields = map[FieldUse][]string{FieldUseFilter: {"missing"}}
	require.ErrorContains(t, ValidateDecisionCatalog(class, invalid), "missing")
	invalid = valid
	invalid.RowPredicates = []TypedPredicate{{Column: "missing", Operator: "eq"}}
	require.ErrorContains(t, ValidateDecisionCatalog(class, invalid), "missing")
}

func TestOperationPolicyProviderIsCalledOnce(t *testing.T) {
	calls := 0
	provider := OperationPolicyProvider(func(context.Context, PolicyRequest) (PolicyDecision, error) {
		calls++
		return PolicyDecision{Allow: true}, nil
	})
	_, err := provider.Decide(context.Background(), PolicyRequest{Identity: mustPolicyIdentity(t, "u"), Route: TenantRoute{Tenant: "t1"}, Coordinate: "Query.x", Resource: "x", Actions: []string{"select"}})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
}

func mustPolicyIdentity(t *testing.T, subject string) Identity {
	t.Helper()
	id, err := NewIdentity(IdentityInput{Tenant: "t1", Subject: subject})
	require.NoError(t, err)
	return id
}
