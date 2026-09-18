package gql

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/require"
)

func testTenantSecurity(router *TenantRouter) TenantGatewayOption {
	states := make(map[string]TenantSecurityState, len(router.routes))
	for tenant, route := range router.routes {
		states[tenant] = TenantSecurityState{MinimumRouteRevision: route.Revision}
	}
	return WithSecurityState(NewRevocationBarrier(router.revision, states))
}

type sharedTenantLoader struct{ global, scoped bool }

func (sharedTenantLoader) Name() string  { return "shared-tenant" }
func (sharedTenantLoader) Support() bool { return true }
func (sharedTenantLoader) Priority() int { return 1 }
func (loader sharedTenantLoader) Load(host protocol.Hoster) error {
	class := &protocol.Class{Name: "Account", Table: "accounts", Global: loader.global, Fields: map[string]*protocol.Field{
		"id": {Name: "id", Column: "id", Type: "integer", IsPrimary: true},
	}}
	if loader.scoped {
		class.Scope = []protocol.ScopeRule{{Column: "tenant_id", Context: "tenant"}}
		class.Permissions = map[string]*protocol.Permission{"reader": {Select: []string{"id"}}}
	}
	return host.PutNode(class.Name, class)
}

func tenantTestEngine(t *testing.T, value string, calls *atomic.Int32) *Engine {
	t.Helper()
	resolver := NewResolver[Root, struct{}, string]("Query", "tenantName", "tenant source",
		func(context.Context, Root, struct{}) (string, error) {
			calls.Add(1)
			return value, nil
		},
		Access("test", "read"),
	)
	builder := New(snapshotKonfig(t), nil, snapshotOptions(snapshotLoader{})...).testAllowAllPolicy()
	require.NoError(t, builder.Register(resolver))
	engine, err := builder.Build()
	require.NoError(t, err)
	t.Cleanup(engine.Close)
	return engine
}

func TestTenantGatewayRoutesBeforeHandlerAndCache(t *testing.T) {
	var aCalls, bCalls atomic.Int32
	a := tenantTestEngine(t, "a", &aCalls)
	b := tenantTestEngine(t, "b", &bCalls)
	router, err := NewTenantRouter(TenantRouterConfig{Mode: TenantModeDatabase, Revision: 1, Routes: map[string]TenantRoute{
		"a": {DataSource: "a"}, "b": {DataSource: "b"},
	}})
	require.NoError(t, err)
	_, err = NewTenantGateway(router, map[string]*Engine{"a": a, "b": b})
	require.ErrorContains(t, err, "SecurityProvider")
	gateway, err := NewTenantGateway(router, map[string]*Engine{"a": a, "b": b}, testTenantSecurity(router))
	require.NoError(t, err)

	execute := func(tenant string) string {
		identity, identityErr := NewIdentity(IdentityInput{Tenant: tenant, Subject: "u"})
		require.NoError(t, identityErr)
		data, executeErr := gateway.Execute(WithIdentity(context.Background(), identity), `{ tenantName }`, nil)
		require.NoError(t, executeErr)
		return string(data)
	}
	require.JSONEq(t, `{"data":{"tenantName":"a"}}`, execute("a"))
	require.JSONEq(t, `{"data":{"tenantName":"b"}}`, execute("b"))
	require.JSONEq(t, `{"data":{"tenantName":"a"}}`, execute("a"))
	require.Equal(t, int32(2), aCalls.Load())
	require.Equal(t, int32(1), bCalls.Load())
}

func TestTenantGatewayOwnsRuntimeInsteadOfHalfInitializedEngine(t *testing.T) {
	engine := tenantTestEngine(t, "a", new(atomic.Int32))
	router, err := NewTenantRouter(TenantRouterConfig{Mode: TenantModeDatabase, Routes: map[string]TenantRoute{"a": {DataSource: "a"}}})
	require.NoError(t, err)
	gateway, err := NewTenantGateway(router, map[string]*Engine{"a": engine}, testTenantSecurity(router))
	require.NoError(t, err)
	require.IsType(t, &TenantGateway{}, gateway)
	require.False(t, engine.rls)
}

func TestTenantGatewayRejectsBeforeHandler(t *testing.T) {
	var calls atomic.Int32
	engine := tenantTestEngine(t, "a", &calls)
	router, err := NewTenantRouter(TenantRouterConfig{Mode: TenantModeDatabase, Routes: map[string]TenantRoute{"a": {DataSource: "a"}}})
	require.NoError(t, err)
	gateway, err := NewTenantGateway(router, map[string]*Engine{"a": engine}, testTenantSecurity(router))
	require.NoError(t, err)

	data, err := gateway.Execute(context.Background(), `{ tenantName }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(data), "identity")
	require.Zero(t, calls.Load())
}

func TestTenantGatewayRevocationBypassesLastKnownGood(t *testing.T) {
	var calls atomic.Int32
	engine := tenantTestEngine(t, "a", &calls)
	router, err := NewTenantRouter(TenantRouterConfig{Mode: TenantModeDatabase, Revision: 1, Routes: map[string]TenantRoute{"a": {DataSource: "a"}}})
	require.NoError(t, err)
	barrier := NewRevocationBarrier(1, map[string]TenantSecurityState{"a": {}})
	gateway, err := NewTenantGateway(router, map[string]*Engine{"a": engine}, WithSecurityState(barrier))
	require.NoError(t, err)
	identity, err := NewIdentity(IdentityInput{Tenant: "a", Subject: "u"})
	require.NoError(t, err)
	ctx := WithIdentity(context.Background(), identity)
	_, err = gateway.Execute(ctx, `{ tenantName }`, nil)
	require.NoError(t, err)
	require.Equal(t, int32(1), calls.Load())
	require.NoError(t, barrier.Replace(2, map[string]TenantSecurityState{"a": {Disabled: true}}))
	data, err := gateway.Execute(ctx, `{ tenantName }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(data), "disabled")
	require.Equal(t, int32(1), calls.Load(), "撤权后旧schema/plan不能继续调用handler")
}

func TestTenantGatewayRejectsUnknownSourceAndSchemaDrift(t *testing.T) {
	var calls atomic.Int32
	engine := tenantTestEngine(t, "a", &calls)
	router, err := NewTenantRouter(TenantRouterConfig{Mode: TenantModeDatabase, Routes: map[string]TenantRoute{"a": {DataSource: "missing"}}})
	require.NoError(t, err)
	_, err = NewTenantGateway(router, map[string]*Engine{"a": engine}, testTenantSecurity(router))
	require.ErrorContains(t, err, "datasource")
}

func TestTenantGatewayRejectsCapabilityDriftWithSameSchema(t *testing.T) {
	build := func(cost uint32) *Engine {
		builder := New(snapshotKonfig(t), nil, snapshotOptions(snapshotLoader{})...).testAllowAllPolicy()
		require.NoError(t, builder.Register(NewResolver[Root, struct{}, string]("Query", "tenantName", "",
			func(context.Context, Root, struct{}) (string, error) { return "tenant", nil }, Cost(cost), Access("test", "read"))))
		engine, err := builder.Build()
		require.NoError(t, err)
		t.Cleanup(engine.Close)
		return engine
	}
	router, err := NewTenantRouter(TenantRouterConfig{Mode: TenantModeDatabase, Routes: map[string]TenantRoute{
		"a": {DataSource: "a"}, "b": {DataSource: "b"},
	}})
	require.NoError(t, err)
	_, err = NewTenantGateway(router, map[string]*Engine{"b": build(2), "a": build(1)}, testTenantSecurity(router))
	require.ErrorContains(t, err, "capability drift")
}

func TestTenantGatewayReportsChildSubscriptionCapability(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	router, err := NewTenantRouter(TenantRouterConfig{Mode: TenantModeDatabase, Routes: map[string]TenantRoute{
		"a": {DataSource: "db"},
	}})
	require.NoError(t, err)
	gateway, err := NewTenantGateway(router, map[string]*Engine{"db": executor}, testTenantSecurity(router))
	require.NoError(t, err)
	require.True(t, executor.SubscriptionAvailable())
	require.True(t, gateway.SubscriptionAvailable(), "dispatcher capability must match its public subscription schema")
	require.NoError(t, executor.database.Pool().Close())
	require.True(t, gateway.SubscriptionAvailable(), "capability discovery must not depend on database health")
}

func TestTenantGatewayHealthChecksRoutedDatasource(t *testing.T) {
	executor, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	router, err := NewTenantRouter(TenantRouterConfig{Mode: TenantModeDatabase, Routes: map[string]TenantRoute{
		"a": {DataSource: "db"},
	}})
	require.NoError(t, err)
	gateway, err := NewTenantGateway(router, map[string]*Engine{"db": executor}, testTenantSecurity(router))
	require.NoError(t, err)
	report, err := gateway.Health(context.Background())
	require.NoError(t, err)
	require.True(t, report.Ready)
	require.NoError(t, db.Pool().Close())
	report, err = gateway.Health(context.Background())
	require.Error(t, err)
	require.False(t, report.Ready)
}

func TestTenantGatewayHealthPreservesWorstCDCState(t *testing.T) {
	idle := tenantTestEngine(t, "idle", new(atomic.Int32))
	connected := tenantTestEngine(t, "connected", new(atomic.Int32))
	idle.cdc = &runtimeStatusNotifier{}
	connectedCDC := &runtimeStatusNotifier{}
	connectedCDC.connected.Store(true)
	connected.cdc = connectedCDC
	router, err := NewTenantRouter(TenantRouterConfig{Mode: TenantModeDatabase, Revision: 1, Routes: map[string]TenantRoute{
		"idle": {DataSource: "idle"}, "connected": {DataSource: "connected"},
	}})
	require.NoError(t, err)
	gateway, err := NewTenantGateway(router, map[string]*Engine{"idle": idle, "connected": connected}, testTenantSecurity(router))
	require.NoError(t, err)

	report, err := gateway.Health(context.Background())
	require.NoError(t, err)
	require.Equal(t, CDCStateDisconnected, report.CDCState)
	require.False(t, report.CDCConnected)
}

func TestTenantGatewayHealthDistinguishesAggregateDegradationFromRouteReadiness(t *testing.T) {
	healthy, healthyDB, healthyCleanup := newTestExecutor(t, nil)
	defer healthyCleanup()
	unhealthy, unhealthyDB, unhealthyCleanup := newTestExecutor(t, nil)
	defer unhealthyCleanup()
	router, err := NewTenantRouter(TenantRouterConfig{Mode: TenantModeDatabase, Revision: 9, Routes: map[string]TenantRoute{
		"healthy": {DataSource: "healthy", Revision: 9},
		"broken":  {DataSource: "broken", Revision: 9},
	}})
	require.NoError(t, err)
	gateway, err := NewTenantGateway(router, map[string]*Engine{"healthy": healthy, "broken": unhealthy}, testTenantSecurity(router))
	require.NoError(t, err)
	require.NoError(t, unhealthyDB.Pool().Close())

	report, err := gateway.Health(context.Background())
	require.NoError(t, err)
	require.True(t, report.Ready, "an idle failed route must not fail process readiness")
	require.True(t, report.Degraded)
	require.Equal(t, []string{"broken"}, report.DegradedSources)
	require.False(t, report.RouteScoped)

	identity, err := NewIdentity(IdentityInput{Tenant: "healthy", Subject: "u"})
	require.NoError(t, err)
	report, err = gateway.Health(WithIdentity(context.Background(), identity))
	require.NoError(t, err)
	require.True(t, report.Ready)
	require.True(t, report.RouteScoped)
	require.Equal(t, uint64(9), report.RouteRevision)

	identity, err = NewIdentity(IdentityInput{Tenant: "broken", Subject: "u"})
	require.NoError(t, err)
	report, err = gateway.Health(WithIdentity(context.Background(), identity))
	require.Error(t, err)
	require.False(t, report.Ready)
	require.True(t, report.RouteScoped)

	require.NoError(t, healthyDB.Pool().Ping())
}

func TestTenantGatewaySharedTableRequiresTenantScopeOrExplicitGlobal(t *testing.T) {
	build := func(loader sharedTenantLoader) *Engine {
		builder := New(snapshotKonfig(t), nil, snapshotOptions(loader)...)
		if !loader.scoped {
			builder = builder.testAllowAllPolicy()
		}
		engine, err := builder.Build()
		require.NoError(t, err)
		return engine
	}
	router, err := NewTenantRouter(TenantRouterConfig{Mode: TenantModeSharedTable, Routes: map[string]TenantRoute{"a": {DataSource: "db"}}})
	require.NoError(t, err)
	_, err = NewTenantGateway(router, map[string]*Engine{"db": build(sharedTenantLoader{})}, testTenantSecurity(router))
	require.ErrorContains(t, err, "tenant scope")
	_, err = NewTenantGateway(router, map[string]*Engine{"db": build(sharedTenantLoader{scoped: true})}, testTenantSecurity(router))
	require.NoError(t, err)
	engine := build(sharedTenantLoader{scoped: true})
	rls := map[string]RLSConfig{"db": {
		Tables: []RLSTable{{Name: "accounts", Protected: true, HasPolicy: true}},
	}}
	gateway, err := NewTenantGateway(router, map[string]*Engine{"db": engine}, testTenantSecurity(router), WithRLS(rls))
	require.NoError(t, err)
	require.False(t, engine.rls, "gateway assembly must not mutate a built child engine")
	delete(rls, "db")
	require.Contains(t, gateway.rls, "db", "gateway must freeze caller-owned RLS configuration")
	_, err = NewTenantGateway(router, map[string]*Engine{"db": build(sharedTenantLoader{scoped: true})}, testTenantSecurity(router), WithRLS(map[string]RLSConfig{"db": {
		Role: RLSRole{BypassRLS: true}, Tables: []RLSTable{{Name: "accounts", Protected: true, HasPolicy: true}},
	}}))
	require.ErrorContains(t, err, "BYPASSRLS")
	_, err = NewTenantGateway(router, map[string]*Engine{"db": build(sharedTenantLoader{global: true})}, testTenantSecurity(router))
	require.NoError(t, err)
}
