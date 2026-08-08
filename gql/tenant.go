package gql

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
)

type TenantMode string

const (
	TenantModeSharedTable TenantMode = "shared-table"
	TenantModeSchema      TenantMode = "schema-per-tenant"
	TenantModeDatabase    TenantMode = "database-per-tenant"
	SharedTable                      = TenantModeSharedTable
	SchemaPerTenant                  = TenantModeSchema
	DatabasePerTenant                = TenantModeDatabase
)

// TenantRoute is a trusted mapping result. Schema and Catalog are SQL
// identifiers and can only originate from router configuration.
type TenantRoute struct {
	Mode       TenantMode
	Tenant     string
	DataSource string
	Schema     string
	Catalog    string
	Revision   uint64
	CacheKey   string
}

type TenantRouterConfig struct {
	Mode     TenantMode
	Revision uint64
	Routes   map[string]TenantRoute
}

type TenantRouter struct {
	mode     TenantMode
	revision uint64
	routes   map[string]TenantRoute
}

func NewTenantRouter(config TenantRouterConfig) (*TenantRouter, error) {
	if config.Mode != TenantModeSharedTable && config.Mode != TenantModeSchema && config.Mode != TenantModeDatabase {
		return nil, fmt.Errorf("unsupported tenant mode %q", config.Mode)
	}
	r := &TenantRouter{mode: config.Mode, revision: config.Revision, routes: make(map[string]TenantRoute, len(config.Routes))}
	for tenant, route := range config.Routes {
		if tenant == "" {
			return nil, fmt.Errorf("tenant route key is required")
		}
		if route.DataSource == "" {
			return nil, fmt.Errorf("tenant %q datasource is required", tenant)
		}
		if strings.IndexByte(route.Schema, 0) >= 0 {
			return nil, fmt.Errorf("tenant %q has unsafe schema identifier", tenant)
		}
		if strings.IndexByte(route.Catalog, 0) >= 0 {
			return nil, fmt.Errorf("tenant %q has unsafe catalog identifier", tenant)
		}
		route.Mode, route.Tenant = config.Mode, tenant
		if route.Revision == 0 {
			route.Revision = config.Revision
		}
		route.CacheKey = routeCacheKey(route)
		r.routes[tenant] = route
	}
	return r, nil
}

func (r *TenantRouter) Route(identity Identity) (TenantRoute, error) {
	if r == nil {
		return TenantRoute{}, fmt.Errorf("tenant router is nil")
	}
	if identity.Tenant() == "" {
		return TenantRoute{}, fmt.Errorf("identity tenant is required")
	}
	route, ok := r.routes[identity.Tenant()]
	if !ok {
		return TenantRoute{}, fmt.Errorf("tenant route not found: %s", identity.Tenant())
	}
	return route, nil
}

// Resolve honors a route pinned to the request context. The pin is accepted
// only when it belongs to the same tenant and has the router's canonical key.
func (r *TenantRouter) Resolve(ctx context.Context, identity Identity) (TenantRoute, error) {
	if pinned, ok := PinnedRoute(ctx); ok {
		current, exists := r.routes[identity.Tenant()]
		if !exists || pinned.Tenant != identity.Tenant() || pinned.CacheKey == "" || pinned.CacheKey != current.CacheKey || pinned.CacheKey != routeCacheKey(pinned) {
			return TenantRoute{}, fmt.Errorf("invalid pinned tenant route")
		}
		return current, nil
	}
	return r.Route(identity)
}

func routeCacheKey(route TenantRoute) string {
	data := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d\x00%s", route.Mode, route.Tenant, route.DataSource, route.Schema, route.Revision, route.Catalog)
	return fmt.Sprintf("tenant:%x", sha256.Sum256([]byte(data)))
}

type pinnedRouteKey struct{}

func PinRoute(ctx context.Context, route TenantRoute) context.Context {
	return context.WithValue(ctx, pinnedRouteKey{}, route)
}

func PinnedRoute(ctx context.Context) (TenantRoute, bool) {
	if ctx == nil {
		return TenantRoute{}, false
	}
	route, ok := ctx.Value(pinnedRouteKey{}).(TenantRoute)
	return route, ok
}

func WithPinnedRoute(ctx context.Context, route TenantRoute) context.Context {
	return PinRoute(ctx, route)
}

func WithTenantRoute(ctx context.Context, route TenantRoute) context.Context {
	return PinRoute(ctx, route)
}

func TenantRouteFromContext(ctx context.Context) (TenantRoute, bool) { return PinnedRoute(ctx) }
