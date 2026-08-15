package gql

import (
	"context"
	"fmt"
	"maps"
	"sync/atomic"
)

// SecurityStateProvider is the single request and subscription revocation
// boundary. Cluster deployments inject a shared, fail-closed implementation.
type SecurityStateProvider interface {
	Check(context.Context, Identity, TenantRoute) error
}

type TenantSecurityState struct {
	Disabled               bool   `json:"disabled"`
	MinimumPolicyRevision  uint64 `json:"minimumPolicyRevision"`
	MinimumRouteRevision   uint64 `json:"minimumRouteRevision"`
	MinimumCredentialEpoch uint64 `json:"minimumCredentialEpoch"`
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
		if current != nil && revision < current.revision {
			return fmt.Errorf("stale revocation revision %d", revision)
		}
		if current != nil && revision == current.revision {
			if maps.Equal(current.tenants, next.tenants) {
				return nil
			}
			return fmt.Errorf("conflicting revocation revision %d", revision)
		}
		if barrier.state.CompareAndSwap(current, next) {
			return nil
		}
	}
}

func (barrier *RevocationBarrier) Check(_ context.Context, identity Identity, route TenantRoute) error {
	if barrier == nil || barrier.state.Load() == nil {
		return &publicFailure{"revocation state unavailable", "SECURITY_STATE_UNAVAILABLE"}
	}
	state, ok := barrier.state.Load().tenants[identity.Tenant()]
	if !ok || route.Tenant != "" && route.Tenant != identity.Tenant() {
		return &publicFailure{"tenant security state unavailable", "SECURITY_STATE_UNAVAILABLE"}
	}
	return checkTenantSecurityState(identity, route, state)
}

func (barrier *RevocationBarrier) Revision(context.Context) (uint64, error) {
	if barrier == nil || barrier.state.Load() == nil {
		return 0, fmt.Errorf("revocation state unavailable")
	}
	return barrier.state.Load().revision, nil
}

func (my *Engine) checkSecurity(ctx context.Context) error {
	if my == nil || my.revocation == nil {
		return nil
	}
	identity, ok := IdentityFromContext(ctx)
	if !ok {
		return &publicFailure{"verified identity is required", "IDENTITY_REQUIRED"}
	}
	route, _ := PinnedRoute(ctx)
	return my.revocation.Check(ctx, identity, route)
}

func cloneTenantStates(states map[string]TenantSecurityState) map[string]TenantSecurityState {
	copy := make(map[string]TenantSecurityState, len(states))
	for tenant, state := range states {
		copy[tenant] = state
	}
	return copy
}
