package gql

import (
	"context"
	"testing"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func emptyMetadata(t *testing.T) *Metadata {
	t.Helper()
	k, err := std.NewKonfig()
	require.NoError(t, err)
	k.Set("mode", "dev")
	k.Set("app.root", t.TempDir())
	meta, err := NewMetadata(k, nil)
	require.NoError(t, err)
	return meta
}

func TestPolicyProviderRequiresExplicitRootResolverAccess(t *testing.T) {
	meta := emptyMetadata(t)
	compiler, err := NewCompiler(meta, nil)
	require.NoError(t, err)
	executor, err := NewExecutor(nil, NewRenderer(meta), meta, compiler)
	require.NoError(t, err)
	provider := OperationPolicyProvider(func(context.Context, PolicyRequest) (PolicyDecision, error) {
		return PolicyDecision{Allow: true}, nil
	})
	require.NoError(t, executor.Register(provider))
	require.NoError(t, executor.Register(NewResolver("Query", "health", "", func(context.Context, Root, struct{}) (bool, error) {
		return true, nil
	})))
	_, err = executor.Snapshot()
	require.ErrorContains(t, err, "Query.health")
	require.ErrorContains(t, err, "policy resource/action")
}

func TestPolicyProviderAcceptsDeclaredRootResolverAccess(t *testing.T) {
	meta := emptyMetadata(t)
	compiler, err := NewCompiler(meta, nil)
	require.NoError(t, err)
	executor, err := NewExecutor(nil, NewRenderer(meta), meta, compiler)
	require.NoError(t, err)
	provider := OperationPolicyProvider(func(context.Context, PolicyRequest) (PolicyDecision, error) {
		return PolicyDecision{Allow: true}, nil
	})
	require.NoError(t, executor.Register(provider))
	require.NoError(t, executor.Register(NewResolver("Query", "health", "", func(context.Context, Root, struct{}) (bool, error) {
		return true, nil
	}, Access("system", "read"))))
	snapshot, err := executor.Snapshot()
	require.NoError(t, err)
	capability, ok := snapshot.Capability("Query.health")
	require.True(t, ok)
	require.Equal(t, "system", capability.Resource)
	require.Equal(t, []string{"read"}, capability.Actions)
}

func TestPolicyProviderDeniesBeforeRootResolver(t *testing.T) {
	meta := emptyMetadata(t)
	compiler, err := NewCompiler(meta, nil)
	require.NoError(t, err)
	executor, err := NewExecutor(nil, NewRenderer(meta), meta, compiler)
	require.NoError(t, err)
	called, handled := 0, false
	provider := OperationPolicyProvider(func(_ context.Context, request PolicyRequest) (PolicyDecision, error) {
		called++
		require.Equal(t, "Query.health", request.Coordinate)
		require.Equal(t, "system", request.Resource)
		require.Equal(t, []string{"read"}, request.Actions)
		return PolicyDecision{Allow: false}, nil
	})
	require.NoError(t, executor.Register(provider))
	require.NoError(t, executor.Register(NewResolver("Query", "health", "", func(context.Context, Root, struct{}) (bool, error) {
		handled = true
		return true, nil
	}, Access("system", "read"))))
	identity, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "u"})
	require.NoError(t, err)
	body, err := executor.Execute(WithIdentity(context.Background(), identity), `{ health }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(body), `Cannot query field \"health\" on type \"Query\"`)
	require.Equal(t, 1, called)
	require.False(t, handled)
}

func TestPolicyProviderRejectsStaleRevisionAndUnawareHandler(t *testing.T) {
	for name, test := range map[string]struct {
		decision PolicyDecision
		want     string
	}{
		"stale":    {PolicyDecision{Allow: true, PolicyRevision: 4}, "revision mismatch"},
		"row fact": {PolicyDecision{Allow: true, RowPredicates: []TypedPredicate{{Column: "id", Operator: "eq", Value: 1}}}, "policy-aware"},
	} {
		t.Run(name, func(t *testing.T) {
			meta := emptyMetadata(t)
			compiler, err := NewCompiler(meta, nil)
			require.NoError(t, err)
			executor, err := NewExecutor(nil, NewRenderer(meta), meta, compiler)
			require.NoError(t, err)
			require.NoError(t, executor.Register(OperationPolicyProvider(func(context.Context, PolicyRequest) (PolicyDecision, error) {
				return test.decision, nil
			})))
			handled := false
			require.NoError(t, executor.Register(NewResolver("Query", "health", "", func(context.Context, Root, struct{}) (bool, error) {
				handled = true
				return true, nil
			}, Access("system", "read"))))
			identity, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "u", PolicyRevision: 5})
			require.NoError(t, err)
			body, err := executor.Execute(WithIdentity(context.Background(), identity), `{ health }`, nil)
			require.NoError(t, err)
			require.Contains(t, string(body), test.want)
			require.False(t, handled)
		})
	}
}

func TestPolicyAwareResolverReceivesDefensiveDecision(t *testing.T) {
	meta := emptyMetadata(t)
	compiler, err := NewCompiler(meta, nil)
	require.NoError(t, err)
	executor, err := NewExecutor(nil, NewRenderer(meta), meta, compiler)
	require.NoError(t, err)
	require.NoError(t, executor.Register(OperationPolicyProvider(func(context.Context, PolicyRequest) (PolicyDecision, error) {
		return PolicyDecision{Allow: true, RowPredicates: []TypedPredicate{{Column: "id", Operator: "eq", Value: 1}}}, nil
	})))
	require.NoError(t, executor.Register(NewResolver("Query", "health", "", func(ctx context.Context, _ Root, _ struct{}) (bool, error) {
		decision, ok := PolicyDecisionFromContext(ctx, "Query.health")
		if !ok {
			return false, nil
		}
		decision.RowPredicates[0].Column = "mutated"
		fresh, _ := PolicyDecisionFromContext(ctx, "Query.health")
		return fresh.RowPredicates[0].Column == "id", nil
	}, Access("system", "read"), ConsumePolicy())))
	identity, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "u"})
	require.NoError(t, err)
	body, err := executor.Execute(WithIdentity(context.Background(), identity), `{ health }`, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"health":true}}`, string(body))
}

func TestPolicyProviderRejectsUnlistedFieldUse(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	provider := OperationPolicyProvider(func(_ context.Context, request PolicyRequest) (PolicyDecision, error) {
		if request.Coordinate != "Query.users" {
			return PolicyDecision{Allow: true, Fields: request.Fields}, nil
		}
		return PolicyDecision{Allow: true, Fields: map[FieldUse][]string{
			FieldUseSelect: {"id"}, FieldUseReturn: {"id"},
		}}, nil
	})
	require.NoError(t, executor.Register(provider))
	identity, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "u"})
	require.NoError(t, err)
	body, err := executor.Execute(WithIdentity(context.Background(), identity), `{ users { items { id name } } }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(body), `Cannot query field \"name\" on type \"User\"`)
	require.NotContains(t, string(body), `"data"`)
}

func TestPolicyProviderCoversNestedRelationResource(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	seen := map[string]PolicyRequest{}
	provider := OperationPolicyProvider(func(_ context.Context, request PolicyRequest) (PolicyDecision, error) {
		seen[request.Coordinate] = request
		return PolicyDecision{Allow: true, Fields: request.Fields}, nil
	})
	require.NoError(t, executor.Register(provider))
	identity, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "u"})
	require.NoError(t, err)
	body, err := executor.Execute(WithIdentity(context.Background(), identity), `{ users { items { id posts { title } } } }`, nil)
	require.NoError(t, err)
	require.NotContains(t, string(body), `"errors"`)
	require.Contains(t, seen, "Query.users")
	require.Contains(t, seen, "User.posts")
	require.Equal(t, "Post", seen["User.posts"].Resource)
	require.Equal(t, []string{"title"}, seen["User.posts"].Fields[FieldUseSelect])
}

func TestPolicyProviderRowPredicateIsCompiledWithRequestValue(t *testing.T) {
	executor, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	require.NoError(t, db.Exec(`INSERT INTO users(name, email) VALUES (?, ?), (?, ?)`,
		"Policy A", "policy-a@x.com", "Policy B", "policy-b@x.com").Error)
	provider := OperationPolicyProvider(func(_ context.Context, request PolicyRequest) (PolicyDecision, error) {
		return PolicyDecision{
			Allow: true, Fields: request.Fields,
			RowPredicates: []TypedPredicate{{Column: "email", Operator: "eq", Value: request.Identity.Subject()}},
		}, nil
	})
	require.NoError(t, executor.Register(provider))
	query := `{ users { items { name } } }`
	for subject, name := range map[string]string{"policy-a@x.com": "Policy A", "policy-b@x.com": "Policy B"} {
		identity, err := NewIdentity(IdentityInput{Tenant: "t", Subject: subject})
		require.NoError(t, err)
		reply := executor.run(WithIdentity(context.Background(), identity), query, nil, "")
		require.Empty(t, reply.Errors, "%v", reply.Errors)
		users := reply.Data["users"].(map[string]any)["items"].([]any)
		require.Len(t, users, 1)
		require.Equal(t, name, users[0].(map[string]any)["name"])
	}
	require.Equal(t, 1, executor.policyCache.order.Len(), "同一策略形状应复用SQL计划，值只走参数槽")
}

func TestPolicyProviderCreateCheckRejectsBeforeWrite(t *testing.T) {
	executor, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	provider := OperationPolicyProvider(func(_ context.Context, request PolicyRequest) (PolicyDecision, error) {
		return PolicyDecision{
			Allow: true, Fields: request.Fields,
			Check: []TypedPredicate{{Column: "name", Operator: "eq", Value: "Allowed"}},
		}, nil
	})
	require.NoError(t, executor.Register(provider))
	identity, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "u"})
	require.NoError(t, err)
	ctx := WithIdentity(context.Background(), identity)
	denied := executor.run(ctx, `mutation { createUser(input: { name: "Denied", email: "policy-denied@x.com" }) { id } }`, nil, "")
	require.NotEmpty(t, denied.Errors)
	var count int64
	require.NoError(t, db.Table("users").Where("email = ?", "policy-denied@x.com").Count(&count).Error)
	require.Zero(t, count)
	allowed := executor.run(ctx, `mutation { createUser(input: { name: "Allowed", email: "policy-allowed@x.com" }) { id } }`, nil, "")
	require.Empty(t, allowed.Errors, "%v", allowed.Errors)
}

func TestPolicyPlanCacheIsPartitionedByTenantRoute(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	seen := map[string]string{}
	require.NoError(t, executor.Register(OperationPolicyProvider(func(_ context.Context, request PolicyRequest) (PolicyDecision, error) {
		seen[request.Identity.Tenant()] = request.Route.CacheKey
		return PolicyDecision{Allow: true, Fields: request.Fields}, nil
	})))
	router, err := NewTenantRouter(TenantRouterConfig{Mode: TenantModeDatabase, Revision: 3, Routes: map[string]TenantRoute{
		"a": {DataSource: "db"}, "b": {DataSource: "db"},
	}})
	require.NoError(t, err)
	gateway, err := NewTenantGateway(router, map[string]*Engine{"db": {Executor: executor}})
	require.NoError(t, err)
	for _, tenant := range []string{"a", "b"} {
		identity, identityErr := NewIdentity(IdentityInput{Tenant: tenant, Subject: "u"})
		require.NoError(t, identityErr)
		body, executeErr := gateway.Execute(WithIdentity(context.Background(), identity), `{ users { items { id } } }`, nil)
		require.NoError(t, executeErr)
		require.NotContains(t, string(body), `"errors"`)
	}
	require.NotEmpty(t, seen["a"])
	require.NotEqual(t, seen["a"], seen["b"])
	require.Equal(t, 2, executor.policyCache.order.Len())
}

func TestPolicyProviderFiltersIntrospectionAndSchemaArtifact(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	require.NoError(t, executor.Register(OperationPolicyProvider(func(_ context.Context, request PolicyRequest) (PolicyDecision, error) {
		fields := map[FieldUse][]string{}
		allowed := []string{"*"}
		for _, use := range request.FieldUses {
			useAllowed := allowed
			if request.Resource == "User" && (use == FieldUseSelect || use == FieldUseReturn || use == FieldUseJoin || use == FieldUseAggregate || use == FieldUseFilter || use == FieldUseGroup) {
				useAllowed = []string{"id"}
			}
			fields[use] = useAllowed
		}
		return PolicyDecision{Allow: true, Fields: fields, PolicyRevision: 7}, nil
	})))
	identity, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "u", PolicyRevision: 7})
	require.NoError(t, err)
	ctx := WithIdentity(context.Background(), identity)
	reply := executor.run(ctx, `{ __type(name: "User") { fields { name } inputFields { name } } }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	typeInfo := reply.Data["__type"].(map[string]any)
	require.Equal(t, []any{map[string]any{"name": "id"}}, typeInfo["fields"])
	reply = executor.run(ctx, `{ __type(name: "UserStats") { fields { name } } }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	require.Equal(t, []any{map[string]any{"name": "key"}, map[string]any{"name": "count"}}, reply.Data["__type"].(map[string]any)["fields"])
	reply = executor.run(ctx, `{ __type(name: "UserDistinctField") { enumValues { name } } }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	require.Equal(t, []any{map[string]any{"name": "id"}}, reply.Data["__type"].(map[string]any)["enumValues"])
	artifact, err := executor.EffectiveSchema(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, artifact.PermissionSignature)
	require.Equal(t, uint64(7), artifact.PolicyRevision)
	require.NotEqual(t, artifact.StructuralFingerprint, artifact.Fingerprint)
	profile, err := executor.GenerateSchemaProfile(ctx, "reader")
	require.NoError(t, err)
	require.NoError(t, profile.ValidateOperation(`query { users { items { id } } }`))
	require.Error(t, profile.ValidateOperation(`query { users { items { name } } }`))
	require.NoError(t, CheckProfileCompatibility(profile, profile))
}

func TestPolicyProviderEnforcesRequestLimitBeforeSQL(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	require.NoError(t, executor.Register(OperationPolicyProvider(func(_ context.Context, request PolicyRequest) (PolicyDecision, error) {
		return PolicyDecision{Allow: true, Fields: request.Fields, Limit: 1}, nil
	})))
	identity, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "u"})
	require.NoError(t, err)
	body, err := executor.Execute(WithIdentity(context.Background(), identity), `{ users(limit: 2) { items { id } } }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(body), "limit 2 exceeds 1")
	require.NotContains(t, string(body), `"data"`)
}

func TestBuiltinPermissionsFilterEffectiveIntrospection(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	user, ok := executor.metadata.GetNode("User")
	require.True(t, ok)
	user.Permissions = map[string]*protocol.Permission{"reader": {Select: []string{"id"}}}
	reply := executor.run(WithRole(context.Background(), "reader"), `{ __type(name: "User") { fields { name } } }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	fields := reply.Data["__type"].(map[string]any)["fields"]
	require.Equal(t, []any{map[string]any{"name": "id"}}, fields)
	reply = executor.run(WithRole(context.Background(), "reader"), `{ __type(name: "UserCreateInput") { name } }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	require.Nil(t, reply.Data["__type"])
	artifact, err := executor.EffectiveSchema(WithRole(context.Background(), "reader"))
	require.NoError(t, err)
	_, err = gqlparser.LoadSchema(&ast.Source{Name: "reader.graphql", Input: artifact.Schema})
	require.NoError(t, err)
}
