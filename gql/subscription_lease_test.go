package gql

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSubscriptionPolicySignatureIncludesPredicateValues(t *testing.T) {
	a := map[string]PolicyDecision{"Query.users": {Allow: true, RowPredicates: []TypedPredicate{{Column: "tenant_id", Operator: "eq", Value: "t1"}}}}
	b := map[string]PolicyDecision{"Query.users": {Allow: true, RowPredicates: []TypedPredicate{{Column: "tenant_id", Operator: "eq", Value: "t2"}}}}
	require.NotEqual(t, policyRuntimeSignature(a), policyRuntimeSignature(b))
}

func TestSubscriptionFeedKeySeparatesIdentityAndPolicy(t *testing.T) {
	variables := map[string]any{"limit": 10}
	scope := map[string]any{"tenant": "t1"}
	id1, err := NewIdentity(IdentityInput{Tenant: "t1", Subject: "u1", Roles: []string{"reader"}, PolicyRevision: 1})
	require.NoError(t, err)
	id2, err := NewIdentity(IdentityInput{Tenant: "t1", Subject: "u2", Roles: []string{"reader"}, PolicyRevision: 1})
	require.NoError(t, err)
	route := TenantRoute{Tenant: "t1", CacheKey: "route-1"}
	a := subscriptionFeedKey("q", "Op", variables, scope, id1, route, "perm-a:shape-a")
	b := subscriptionFeedKey("q", "Op", variables, scope, id2, route, "perm-a:shape-a")
	c := subscriptionFeedKey("q", "Op", variables, scope, id1, route, "perm-b:shape-a")
	require.NotEqual(t, a, b)
	require.NotEqual(t, a, c)
}

func TestSubscriptionLeaseStopsAfterRevocation(t *testing.T) {
	identity, err := NewIdentity(IdentityInput{Tenant: "t1", Subject: "u", PolicyRevision: 1})
	require.NoError(t, err)
	barrier := NewRevocationBarrier(1, map[string]TenantSecurityState{"t1": {MinimumPolicyRevision: 1}})
	lease := subscriptionLease{Barrier: barrier, Identity: identity, Route: TenantRoute{Tenant: "t1", Revision: 1}, Interval: time.Millisecond}
	require.NoError(t, lease.Check(context.Background()))
	require.NoError(t, barrier.Replace(2, map[string]TenantSecurityState{"t1": {Disabled: true}}))
	require.Error(t, lease.Check(context.Background()))
}

func TestSubscriptionLeaseExpiresWithoutDatabaseWake(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	executor.revocation = NewRevocationBarrier(1, map[string]TenantSecurityState{"t1": {}})
	require.NoError(t, ConfigureSubscriptionLease(executor, 10*time.Millisecond))
	identity, err := NewIdentity(IdentityInput{Tenant: "t1", Subject: "u", PolicyRevision: 1})
	require.NoError(t, err)
	route := TenantRoute{Tenant: "t1", Revision: 1, CacheKey: "route"}
	ctx, cancel := context.WithCancel(PinRoute(WithIdentity(context.Background(), identity), route))
	defer cancel()
	events, err := executor.subscribe(ctx, `subscription { users { total } }`, nil, "")
	require.NoError(t, err)
	select {
	case first := <-events:
		require.Empty(t, first.Errors)
	case <-time.After(3 * time.Second):
		t.Fatal("missing initial subscription result")
	}
	require.NoError(t, executor.revocation.Replace(2, map[string]TenantSecurityState{"t1": {Disabled: true}}))
	select {
	case expired, ok := <-events:
		require.True(t, ok)
		require.NotEmpty(t, expired.Errors)
		require.Contains(t, expired.Errors.Error(), "disabled")
		_, ok = <-events
		require.False(t, ok)
	case <-time.After(time.Second):
		t.Fatal("lease did not expire without a CDC wake")
	}
}
