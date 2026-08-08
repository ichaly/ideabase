package gql

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRevocationBarrierFailsClosedAndUpdatesAtomically(t *testing.T) {
	identity, err := NewIdentity(IdentityInput{Tenant: "a", Subject: "u", PolicyRevision: 4, CredentialEpoch: 2})
	require.NoError(t, err)
	route := TenantRoute{Tenant: "a", Revision: 3}
	barrier := NewRevocationBarrier(1, map[string]TenantSecurityState{"a": {
		MinimumPolicyRevision: 4, MinimumRouteRevision: 3, MinimumCredentialEpoch: 2,
	}})
	require.NoError(t, barrier.Check(identity, route))
	require.Error(t, barrier.Check(identity, TenantRoute{Tenant: "missing", Revision: 3}))
	require.Error(t, barrier.Check(mustSecurityIdentity(t, 3, 2), route))
	require.Error(t, barrier.Check(mustSecurityIdentity(t, 4, 1), route))

	require.NoError(t, barrier.Replace(2, map[string]TenantSecurityState{"a": {Disabled: true}}))
	require.ErrorContains(t, barrier.Check(identity, route), "disabled")
	require.Error(t, barrier.Replace(1, map[string]TenantSecurityState{"a": {}}), "旧控制面revision不得覆盖新撤权")
}

func mustSecurityIdentity(t *testing.T, policy, credential uint64) Identity {
	t.Helper()
	identity, err := NewIdentity(IdentityInput{Tenant: "a", Subject: "u", PolicyRevision: policy, CredentialEpoch: credential})
	require.NoError(t, err)
	return identity
}
