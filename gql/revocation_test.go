package gql

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type securityStateFunc func(context.Context, Identity, TenantRoute) error

func (check securityStateFunc) Check(ctx context.Context, identity Identity, route TenantRoute) error {
	return check(ctx, identity, route)
}

func TestRevocationBarrierFailsClosedAndUpdatesAtomically(t *testing.T) {
	identity, err := NewIdentity(IdentityInput{Tenant: "a", Subject: "u", PolicyRevision: 4, CredentialEpoch: 2})
	require.NoError(t, err)
	route := TenantRoute{Tenant: "a", Revision: 3}
	barrier := NewRevocationBarrier(1, map[string]TenantSecurityState{"a": {
		MinimumPolicyRevision: 4, MinimumRouteRevision: 3, MinimumCredentialEpoch: 2,
	}})
	require.NoError(t, barrier.Check(context.Background(), identity, route))
	require.Error(t, barrier.Check(context.Background(), identity, TenantRoute{Tenant: "missing", Revision: 3}))
	require.Error(t, barrier.Check(context.Background(), mustSecurityIdentity(t, 3, 2), route))
	require.Error(t, barrier.Check(context.Background(), mustSecurityIdentity(t, 4, 1), route))

	require.NoError(t, barrier.Replace(2, map[string]TenantSecurityState{"a": {Disabled: true}}))
	require.NoError(t, barrier.Replace(2, map[string]TenantSecurityState{"a": {Disabled: true}}), "identical control-plane replay must be idempotent")
	require.ErrorContains(t, barrier.Replace(2, map[string]TenantSecurityState{"a": {}}), "conflicting")
	require.ErrorContains(t, barrier.Check(context.Background(), identity, route), "disabled")
	require.Error(t, barrier.Replace(1, map[string]TenantSecurityState{"a": {}}), "旧控制面revision不得覆盖新撤权")
}

func mustSecurityIdentity(t *testing.T, policy, credential uint64) Identity {
	t.Helper()
	identity, err := NewIdentity(IdentityInput{Tenant: "a", Subject: "u", PolicyRevision: policy, CredentialEpoch: credential})
	require.NoError(t, err)
	return identity
}

func TestSecurityStateGuardsExecutionAndEffectiveIntrospection(t *testing.T) {
	engine, cleanup := setupTestExecutor(t)
	defer cleanup()
	revoked := errors.New("shared security state revoked")
	engine.revocation = securityStateFunc(func(context.Context, Identity, TenantRoute) error { return revoked })
	identity, err := NewIdentity(IdentityInput{Tenant: "a", Subject: "u"})
	require.NoError(t, err)
	ctx := WithIdentity(context.Background(), identity)

	_, err = engine.EffectiveSchema(ctx)
	require.ErrorIs(t, err, revoked)
	body, err := engine.Execute(ctx, `{ users { total } }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(body), `"errors"`)
}
