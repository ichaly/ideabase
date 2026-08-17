package gql

import (
	"context"
	"fmt"
	"sync/atomic"
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

func TestSubscriptionFeedKeySeparatesCredentialEpoch(t *testing.T) {
	old, err := NewIdentity(IdentityInput{Tenant: "t1", Subject: "u1", PolicyRevision: 1, CredentialEpoch: 1})
	require.NoError(t, err)
	current, err := NewIdentity(IdentityInput{Tenant: "t1", Subject: "u1", PolicyRevision: 1, CredentialEpoch: 2})
	require.NoError(t, err)
	route := TenantRoute{Tenant: "t1", CacheKey: "route-1"}
	require.NotEqual(t,
		subscriptionFeedKey("q", "Op", nil, nil, old, route, "policy"),
		subscriptionFeedKey("q", "Op", nil, nil, current, route, "policy"),
	)
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
	barrier := NewRevocationBarrier(1, map[string]TenantSecurityState{"t1": {}})
	executor.revocation = barrier
	executor.subscriptionLease = 10 * time.Millisecond
	identity, err := NewIdentity(IdentityInput{Tenant: "t1", Subject: "u", PolicyRevision: 1})
	require.NoError(t, err)
	route := TenantRoute{Tenant: "t1", Revision: 1, CacheKey: "route"}
	ctx, cancel := context.WithCancel(PinRoute(WithIdentity(context.Background(), identity), route))
	defer cancel()
	events, err := executor.subscribe(ctx, `subscription { users { total } }`, nil, "")
	require.NoError(t, err)
	select {
	case first := <-events:
		first = decodeSubscriptionReply(t, first)
		require.Empty(t, first.Errors)
	case <-time.After(3 * time.Second):
		t.Fatal("missing initial subscription result")
	}
	require.NoError(t, barrier.Replace(2, map[string]TenantSecurityState{"t1": {Disabled: true}}))
	select {
	case expired, ok := <-events:
		require.True(t, ok)
		expired = decodeSubscriptionReply(t, expired)
		require.NotEmpty(t, expired.Errors)
		require.Contains(t, expired.Errors.Error(), "disabled")
		_, ok = <-events
		require.False(t, ok)
	case <-time.After(time.Second):
		t.Fatal("lease did not expire without a CDC wake")
	}
}

func TestSubscriptionWakeChecksLeaseBeforeFetch(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	barrier := NewRevocationBarrier(1, map[string]TenantSecurityState{"t1": {}})
	executor.revocation = barrier
	identity, err := NewIdentity(IdentityInput{Tenant: "t1", Subject: "u", PolicyRevision: 1})
	require.NoError(t, err)
	route := TenantRoute{Tenant: "t1", Revision: 1, CacheKey: "route"}
	ctx, cancel := context.WithCancel(PinRoute(WithIdentity(context.Background(), identity), route))
	defer cancel()
	plan, err := executor.plan(`subscription { users { total } }`, "", nil)
	require.NoError(t, err)
	watch := &watcher{wake: make(chan struct{}, 1)}
	events := make(chan gqlReply, 2)
	feed := &feed{subs: map[chan gqlReply]bool{events: true}, stop: cancel, done: ctx.Done()}
	lease := subscriptionLease{Barrier: barrier, Identity: identity, Route: route, Interval: time.Hour}
	streamCtx := withSubscriptionLease(ctx, lease)
	executor.streamWG.Add(1)
	go executor.stream(streamCtx, plan, nil, watch, feed)
	select {
	case first := <-events:
		first = decodeSubscriptionReply(t, first)
		require.Empty(t, first.Errors)
	case <-time.After(3 * time.Second):
		t.Fatal("missing initial subscription result")
	}

	require.NoError(t, barrier.Replace(2, map[string]TenantSecurityState{"t1": {Disabled: true}}))

	// The lease interval is deliberately long: only the CDC wake can end it.
	watch.wake <- struct{}{}
	select {
	case expired, ok := <-events:
		require.True(t, ok)
		expired = decodeSubscriptionReply(t, expired)
		require.Contains(t, expired.Errors.Error(), "disabled")
	case <-time.After(time.Second):
		t.Fatal("revoked subscription did not publish an error")
	}
}

func TestSubscriptionWaitsForBoundedCDCReconnect(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	plan, err := executor.plan(`subscription { users { total } }`, "", nil)
	require.NoError(t, err)
	connected := atomic.Bool{}
	connected.Store(true)
	outage := atomic.Int64{}
	watch := &watcher{wake: make(chan struct{}, 1), status: func() (bool, string) {
		if connected.Load() {
			return true, ""
		}
		return false, "CDC replication connection lost"
	}, outage: func() time.Duration { return time.Duration(outage.Load()) }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan gqlReply, 2)
	feed := &feed{subs: map[chan gqlReply]bool{events: true}, stop: cancel, done: ctx.Done()}
	executor.streamWG.Add(1)
	go executor.stream(ctx, plan, nil, watch, feed)
	select {
	case first := <-events:
		first = decodeSubscriptionReply(t, first)
		require.Empty(t, first.Errors)
	case <-time.After(3 * time.Second):
		t.Fatal("missing initial subscription result")
	}
	connected.Store(false)
	watch.wake <- struct{}{}
	select {
	case event := <-events:
		event = decodeSubscriptionReply(t, event)
		t.Fatalf("transient disconnect unexpectedly ended feed: %v", event.Errors)
	case <-time.After(50 * time.Millisecond):
	}
	outage.Store(int64(cdcReconnectGrace))
	watch.wake <- struct{}{}
	failed, ok := <-events
	require.True(t, ok)
	failed = decodeSubscriptionReply(t, failed)
	require.Contains(t, fmt.Sprint(failed.Errors), "CDC replication connection lost")
}
