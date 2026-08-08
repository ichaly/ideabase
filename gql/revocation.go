package gql

import (
	"fmt"
	"sync/atomic"
)

type TenantSecurityState struct {
	Disabled               bool
	MinimumPolicyRevision  uint64
	MinimumRouteRevision   uint64
	MinimumCredentialEpoch uint64
}

type revocationSnapshot struct {
	revision uint64
	tenants  map[string]TenantSecurityState
}

// RevocationBarrier is an independently updated security epoch. Unlike a
// schema snapshot it has no last-known-good fallback: every request checks the
// latest atomically published tenant state.
type RevocationBarrier struct {
	state atomic.Pointer[revocationSnapshot]
}

func NewRevocationBarrier(revision uint64, tenants map[string]TenantSecurityState) *RevocationBarrier {
	barrier := &RevocationBarrier{}
	barrier.state.Store(&revocationSnapshot{revision: revision, tenants: cloneTenantStates(tenants)})
	return barrier
}

func (barrier *RevocationBarrier) Replace(revision uint64, tenants map[string]TenantSecurityState) error {
	if barrier == nil {
		return fmt.Errorf("revocation barrier is nil")
	}
	next := &revocationSnapshot{revision: revision, tenants: cloneTenantStates(tenants)}
	for {
		current := barrier.state.Load()
		if current != nil && revision <= current.revision {
			return fmt.Errorf("stale revocation revision %d", revision)
		}
		if barrier.state.CompareAndSwap(current, next) {
			return nil
		}
	}
}

func (barrier *RevocationBarrier) Check(identity Identity, route TenantRoute) error {
	if barrier == nil || barrier.state.Load() == nil {
		return fmt.Errorf("revocation state unavailable")
	}
	state, ok := barrier.state.Load().tenants[identity.Tenant()]
	if !ok || route.Tenant != "" && route.Tenant != identity.Tenant() {
		return fmt.Errorf("tenant security state unavailable")
	}
	if state.Disabled {
		return fmt.Errorf("tenant is disabled")
	}
	if identity.PolicyRevision() < state.MinimumPolicyRevision {
		return fmt.Errorf("identity policy revision is revoked")
	}
	if identity.CredentialEpoch() < state.MinimumCredentialEpoch {
		return fmt.Errorf("identity credential epoch is revoked")
	}
	if route.Revision < state.MinimumRouteRevision {
		return fmt.Errorf("tenant route revision is revoked")
	}
	return nil
}

func cloneTenantStates(states map[string]TenantSecurityState) map[string]TenantSecurityState {
	copy := make(map[string]TenantSecurityState, len(states))
	for tenant, state := range states {
		copy[tenant] = state
	}
	return copy
}
