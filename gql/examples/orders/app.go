package main

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/ichaly/ideabase/gql"
	_ "github.com/ichaly/ideabase/gql/compiler/pgsql"
	"github.com/ichaly/ideabase/std"
)

// Permissions are role-based; tenant isolation is enforced by scope and RLS.
type orderPolicy struct{}

func (orderPolicy) Decide(_ context.Context, r gql.PolicyRequest) (gql.PolicyDecision, error) {
	roles := r.Identity.Roles()
	read := slices.Contains(roles, "viewer") || slices.Contains(roles, "operator")
	allow := read
	for _, action := range r.Actions {
		if action != "select" && action != "aggregate" && action != "read" {
			allow = allow && slices.Contains(roles, "operator") && ((r.Resource == "orders" && (action == "place" || action == "cancel")) || (r.Resource == "Customer" && (action == "insert" || action == "update")))
		}
	}
	fields := make(map[gql.FieldUse][]string, len(r.FieldUses))
	for _, use := range r.FieldUses {
		fields[use] = []string{"*"}
	}
	return gql.PolicyDecision{Allow: allow, Fields: fields}, nil
}
func (orderPolicy) PolicyCacheIdentity(_ context.Context, id gql.Identity, _ gql.TenantRoute, _ map[string]any) (string, bool) {
	roles := id.Roles()
	slices.Sort(roles)
	return strings.Join(roles, "\x00"), true
}

// Same assembly for the executable, integration tests, composed gateways and replicas.
func buildEndpoint(db *gql.Database, cdcDSN string, tenants []string, observer gql.Observer) (*gql.Engine, *gql.TenantGateway, error) {
	db.Pool().SetMaxOpenConns(16)
	db.Pool().SetMaxIdleConns(16)
	db.Pool().SetConnMaxLifetime(30 * time.Minute)
	k, err := std.NewKonfig()
	if err != nil {
		return nil, nil, err
	}
	k.Set("mode", "prod")
	k.Set("subscription.dsn", cdcDSN)
	k.Set("schema.default-limit", 20)
	k.Set("schema.max-depth", 8)
	k.Set("schema.max-cost", 10000)
	k.Set("metadata.include-tables", []string{"customers", "products", "orders", "order_items"})
	classes := map[string]any{}
	for name, table := range map[string]string{"Customer": "customers", "Product": "products", "Order": "orders", "OrderItem": "order_items"} {
		classes[name] = map[string]any{"table": table, "operations": []string{"select", "aggregate"}, "scope": []map[string]string{{"column": "tenant_id", "context": "tenant"}}}
	}
	classes["Customer"].(map[string]any)["operations"] = []string{"select", "aggregate", "insert", "update"}
	k.Set("metadata.classes", classes)
	security, err := gql.NewPostgresSecurityStateProvider(db)
	if err != nil {
		return nil, nil, err
	}
	builder := gql.New(k, db)
	if err = builder.Policy(orderPolicy{}); err != nil {
		return nil, nil, err
	}
	if err = builder.Security(security); err != nil {
		return nil, nil, err
	}
	if err = builder.Observe(observer); err != nil {
		return nil, nil, err
	}
	if err = builder.Register(
		gql.NewResolver("Mutation", "placeOrder", "下单并扣减库存", placeOrder, gql.Access("orders", "place")),
		gql.NewResolver("Mutation", "cancelOrder", "取消并回补库存", cancelOrder, gql.Access("orders", "cancel")),
	); err != nil {
		return nil, nil, err
	}
	engine, err := builder.Build()
	if err != nil {
		return nil, nil, err
	}
	routes := make(map[string]gql.TenantRoute, len(tenants))
	for _, tenant := range tenants {
		routes[tenant] = gql.TenantRoute{DataSource: "orders"}
	}
	router, err := gql.NewTenantRouter(gql.TenantRouterConfig{Mode: gql.TenantModeSharedTable, Revision: 1, Routes: routes})
	if err != nil {
		engine.Close()
		return nil, nil, err
	}
	endpoint, err := gql.NewTenantGateway(router, map[string]*gql.Engine{"orders": engine}, gql.WithSecurityState(security))
	if err != nil {
		engine.Close()
		return nil, nil, err
	}
	return engine, endpoint, nil
}
