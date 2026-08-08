package gql

import (
	"context"
	"testing"

	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
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
	require.JSONEq(t, `{"errors":[{"message":"policy denied Query.health"}]}`, string(body))
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
		require.Equal(t, "Query.users", request.Coordinate)
		require.ElementsMatch(t, []string{"id", "name"}, request.Fields[FieldUseSelect])
		return PolicyDecision{Allow: true, Fields: map[FieldUse][]string{
			FieldUseSelect: {"id"}, FieldUseReturn: {"id"},
		}}, nil
	})
	require.NoError(t, executor.Register(provider))
	identity, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "u"})
	require.NoError(t, err)
	body, err := executor.Execute(WithIdentity(context.Background(), identity), `{ users { items { id name } } }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(body), "field-use=return field=name")
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
