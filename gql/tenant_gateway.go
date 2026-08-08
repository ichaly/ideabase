package gql

import (
	"context"
	"fmt"
)

// NewTenantGateway builds a routing-only Engine over independently built,
// immutable engines. Each datasource keeps its own pool, catalog, plan cache,
// CDC and transaction boundary; the gateway routes before parsing or calling
// any handler. All sources must expose the same GraphQL contract.
type tenantGatewayConfig struct{ revocation *RevocationBarrier }
type TenantGatewayOption func(*tenantGatewayConfig)

func WithRevocationBarrier(barrier *RevocationBarrier) TenantGatewayOption {
	return func(config *tenantGatewayConfig) { config.revocation = barrier }
}

func NewTenantGateway(router *TenantRouter, sources map[string]*Engine, options ...TenantGatewayOption) (*Engine, error) {
	if router == nil {
		return nil, fmt.Errorf("tenant router is required")
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("tenant datasource engines are required")
	}
	targets := make(map[string]*Executor, len(sources))
	var canonical *Snapshot
	for name, engine := range sources {
		if name == "" || engine == nil || engine.Executor == nil {
			return nil, fmt.Errorf("tenant datasource %q is invalid", name)
		}
		snapshot, err := engine.Snapshot()
		if err != nil {
			return nil, fmt.Errorf("tenant datasource %q snapshot: %w", name, err)
		}
		if canonical == nil {
			canonical = snapshot
		} else if snapshot.Schema() != canonical.Schema() {
			return nil, fmt.Errorf("tenant datasource %q schema drift", name)
		}
		targets[name] = engine.Executor
	}
	for tenant, route := range router.routes {
		target := targets[route.DataSource]
		if target == nil {
			return nil, fmt.Errorf("tenant %q datasource %q is not registered", tenant, route.DataSource)
		}
		if route.Catalog != "" {
			fingerprint, err := target.metadata.CatalogFingerprint()
			if err != nil || fingerprint != route.Catalog {
				return nil, fmt.Errorf("tenant %q catalog fingerprint mismatch", tenant)
			}
		}
		if router.mode == TenantModeSchema && (route.Schema == "" || route.Schema != target.metadata.SchemaName()) {
			return nil, fmt.Errorf("tenant %q schema route does not match datasource catalog", tenant)
		}
		if router.mode == TenantModeSharedTable {
			if err := validateSharedTenantScope(target.metadata); err != nil {
				return nil, err
			}
		}
	}
	config := tenantGatewayConfig{}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	dispatcher := &Executor{tenantRouter: router, tenantSources: targets, revocation: config.revocation, snapshot: canonical}
	dispatcher.frozen.Store(true)
	return &Engine{Executor: dispatcher}, nil
}

func validateSharedTenantScope(metadata *Metadata) error {
	for _, name := range sortedNodeNames(metadata) {
		class := metadata.Nodes[name]
		if class == nil || name != class.Name || class.Table == "" || class.Global {
			continue
		}
		scoped := false
		for _, scope := range class.Scope {
			if scope.Context == "tenant" {
				scoped = true
				break
			}
		}
		if !scoped {
			return fmt.Errorf("shared-table entity %s requires tenant scope or explicit global", class.Name)
		}
	}
	return nil
}

func (my *Executor) tenantTarget(ctx context.Context) (*Executor, error) {
	if my.tenantRouter == nil {
		return my, nil
	}
	identity, ok := IdentityFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("verified identity is required for tenant gateway")
	}
	route, err := my.tenantRouter.Resolve(ctx, identity)
	if err != nil {
		return nil, err
	}
	if my.revocation != nil {
		if err := my.revocation.Check(identity, route); err != nil {
			return nil, err
		}
	}
	target := my.tenantSources[route.DataSource]
	if target == nil {
		return nil, fmt.Errorf("tenant datasource is unavailable")
	}
	return target, nil
}
