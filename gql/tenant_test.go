package gql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIdentityIsHostConstructedAndImmutable(t *testing.T) {
	roles := []string{"reader"}
	claims := map[string]any{"region": "cn", "nested": map[string]any{"ok": true}}
	id, err := NewIdentity(IdentityInput{Tenant: "acme", Subject: "user-1", Actor: "svc", Roles: roles, Claims: claims, PolicyRevision: 7, RequestID: "req-1"})
	require.NoError(t, err)
	roles[0] = "admin"
	claims["nested"].(map[string]any)["ok"] = false
	gotRoles := id.Roles()
	gotClaims := id.Claims()
	gotRoles[0] = "writer"
	gotClaims["region"] = "evil"
	require.Equal(t, []string{"reader"}, id.Roles())
	require.Equal(t, "cn", id.Claims()["region"])
	require.Equal(t, true, id.Claims()["nested"].(map[string]any)["ok"])
	require.Equal(t, uint64(7), id.PolicyRevision())
}

func TestIdentityRejectsMutableNonJSONClaims(t *testing.T) {
	value := 1
	_, err := NewIdentity(IdentityInput{Tenant: "acme", Subject: "u", Claims: map[string]any{"pointer": &value}})
	require.ErrorContains(t, err, "claim")
}

func TestTenantRouterUsesTrustedMappingAndStableCacheKey(t *testing.T) {
	router, err := NewTenantRouter(TenantRouterConfig{
		Mode:     TenantModeSchema,
		Revision: 9,
		Routes:   map[string]TenantRoute{"acme": {DataSource: "primary", Schema: "tenant_acme", Catalog: "catalog_v1"}},
	})
	require.NoError(t, err)
	id, err := NewIdentity(IdentityInput{Tenant: "acme", Subject: "u"})
	require.NoError(t, err)
	route, err := router.Route(id)
	require.NoError(t, err)
	require.Equal(t, "tenant_acme", route.Schema)
	require.Equal(t, uint64(9), route.Revision)
	require.NotEmpty(t, route.CacheKey)
	other, err := router.Route(id)
	require.NoError(t, err)
	require.Equal(t, route.CacheKey, other.CacheKey)

	for _, tenant := range []string{"tenant_acme", "x;drop table users", "a\".b"} {
		spoof, err := NewIdentity(IdentityInput{Tenant: tenant, Subject: "u"})
		if err != nil {
			continue
		}
		_, err = router.Route(spoof)
		require.Error(t, err)
	}
}

func TestTenantRouterSupportsAllModesAndPin(t *testing.T) {
	for _, mode := range []TenantMode{TenantModeSharedTable, TenantModeSchema, TenantModeDatabase} {
		router, err := NewTenantRouter(TenantRouterConfig{Mode: mode, Revision: 3, Routes: map[string]TenantRoute{
			"a": {DataSource: "db-a", Schema: "tenant_a", Catalog: "cat_a"},
		}})
		require.NoError(t, err)
		id, err := NewIdentity(IdentityInput{Tenant: "a", Subject: "u"})
		require.NoError(t, err)
		route, err := router.Route(id)
		require.NoError(t, err)
		require.Equal(t, mode, route.Mode)
		ctx := PinRoute(context.Background(), route)
		pinned, ok := PinnedRoute(ctx)
		require.True(t, ok)
		require.Equal(t, route, pinned)
	}
}

func TestTenantRouterAcceptsTrustedQuotedIdentifier(t *testing.T) {
	router, err := NewTenantRouter(TenantRouterConfig{Mode: TenantModeSchema, Routes: map[string]TenantRoute{
		"a": {DataSource: "db", Schema: "Tenant Case"},
	}})
	require.NoError(t, err)
	id, err := NewIdentity(IdentityInput{Tenant: "a", Subject: "u"})
	require.NoError(t, err)
	route, err := router.Route(id)
	require.NoError(t, err)
	require.Equal(t, "Tenant Case", route.Schema)
}

func TestTenantRouterRejectsStalePinnedRoute(t *testing.T) {
	id, err := NewIdentity(IdentityInput{Tenant: "a", Subject: "u"})
	require.NoError(t, err)
	oldRouter, err := NewTenantRouter(TenantRouterConfig{Mode: TenantModeSchema, Revision: 1, Routes: map[string]TenantRoute{
		"a": {DataSource: "db", Schema: "tenant_a"},
	}})
	require.NoError(t, err)
	stale, err := oldRouter.Route(id)
	require.NoError(t, err)
	current, err := NewTenantRouter(TenantRouterConfig{Mode: TenantModeSchema, Revision: 2, Routes: map[string]TenantRoute{
		"a": {DataSource: "db", Schema: "tenant_a"},
	}})
	require.NoError(t, err)
	_, err = current.Resolve(PinRoute(context.Background(), stale), id)
	require.ErrorContains(t, err, "pinned")
}
