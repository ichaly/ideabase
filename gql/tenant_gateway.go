package gql

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
)

// NewTenantGateway builds a routing-only Engine over independently built,
// immutable engines. Each datasource keeps its own pool, catalog, plan cache,
// CDC and transaction boundary; the gateway routes before parsing or calling
// any handler. All sources must expose the same GraphQL contract.
type tenantGatewayConfig struct {
	revocation SecurityProvider
	rls        map[string]RLSConfig
}
type TenantGatewayOption func(*tenantGatewayConfig)

func WithSecurityState(provider SecurityProvider) TenantGatewayOption {
	return func(config *tenantGatewayConfig) { config.revocation = provider }
}

// WithRLS validates PostgreSQL role/table facts for shared-table datasources
// and enables transaction-local verified identity on those source engines.
func WithRLS(config map[string]RLSConfig) TenantGatewayOption {
	return func(options *tenantGatewayConfig) { options.rls = maps.Clone(config) }
}

// TenantGateway is a routing runtime. It deliberately does not embed Engine:
// a gateway has no compiler, database, policy or partially initialized caches.
type TenantGateway struct {
	router     *TenantRouter
	sources    map[string]*Engine
	snapshot   *Snapshot
	revocation SecurityProvider
	rls        map[string]struct{}
}

type tenantRLSKey struct{}

func tenantRLSContext(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, tenantRLSKey{}, source)
}

func tenantRLSRequested(ctx context.Context) bool {
	_, ok := ctx.Value(tenantRLSKey{}).(string)
	return ok
}

func NewTenantGateway(router *TenantRouter, sources map[string]*Engine, options ...TenantGatewayOption) (*TenantGateway, error) {
	if router == nil {
		return nil, fmt.Errorf("tenant router is required")
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("tenant datasource engines are required")
	}
	targets := make(map[string]*Engine, len(sources))
	var canonical *Snapshot
	for _, name := range slices.Sorted(maps.Keys(sources)) {
		engine := sources[name]
		if name == "" || engine == nil {
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
		} else if !reflect.DeepEqual(snapshot.Capabilities(), canonical.Capabilities()) {
			return nil, fmt.Errorf("tenant datasource %q capability drift", name)
		}
		targets[name] = engine
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
	if config.revocation == nil {
		return nil, fmt.Errorf("tenant SecurityProvider is required")
	}
	if router.mode == TenantModeSharedTable {
		enabled := make(map[string]struct{}, len(config.rls))
		for source, target := range targets {
			rls, configured := config.rls[source]
			production := target.database != nil && target.database.Driver() == "postgres" && target.metadata.cfg != nil && !target.metadata.cfg.IsDebug()
			if !configured && production {
				var err error
				rls, err = InspectRLS(target.database, target.metadata)
				if err != nil {
					return nil, fmt.Errorf("shared-table datasource %s: %w", source, err)
				}
				configured = true
			}
			if !configured {
				continue
			}
			if err := ValidateRLS(rls); err != nil {
				return nil, fmt.Errorf("shared-table datasource %s: %w", source, err)
			}
			if err := validateRLSTables(target.metadata, rls.Tables); err != nil {
				return nil, fmt.Errorf("shared-table datasource %s: %w", source, err)
			}
			enabled[source] = struct{}{}
		}
		return &TenantGateway{router: router, sources: targets, snapshot: canonical, revocation: config.revocation, rls: enabled}, nil
	}
	return &TenantGateway{router: router, sources: targets, snapshot: canonical, revocation: config.revocation}, nil
}

func (my *TenantGateway) EndpointID() string { return "tenant-gateway" }
func (my *TenantGateway) Snapshot() (*Snapshot, error) {
	if my == nil || my.snapshot == nil {
		return nil, fmt.Errorf("gql snapshot is unavailable")
	}
	return my.snapshot, nil
}
func (my *TenantGateway) route(ctx context.Context) (*Engine, context.Context, TenantRoute, error) {
	if my == nil || my.router == nil {
		return nil, ctx, TenantRoute{}, fmt.Errorf("tenant gateway is unavailable")
	}
	identity, ok := IdentityFromContext(ctx)
	if !ok {
		return nil, ctx, TenantRoute{}, &publicFailure{"verified identity is required for tenant gateway", "IDENTITY_REQUIRED"}
	}
	route, err := my.router.Resolve(ctx, identity)
	if err != nil {
		return nil, ctx, TenantRoute{}, err
	}
	if my.revocation != nil {
		if err := my.revocation.Check(ctx, identity, route); err != nil {
			return nil, ctx, TenantRoute{}, err
		}
	}
	target := my.sources[route.DataSource]
	if target == nil {
		return nil, ctx, TenantRoute{}, fmt.Errorf("tenant datasource is unavailable")
	}
	ctx = PinRoute(ctx, route)
	if _, ok := my.rls[route.DataSource]; ok {
		ctx = tenantRLSContext(ctx, route.DataSource)
	}
	return target, ctx, route, nil
}
func (my *TenantGateway) Execute(ctx context.Context, query string, variables map[string]any) ([]byte, error) {
	target, routed, _, err := my.route(ctx)
	if err != nil {
		return marshalGatewayError(err)
	}
	return target.ExecuteOperation(routed, query, variables, "")
}
func (my *TenantGateway) ExecuteOperation(ctx context.Context, query string, variables map[string]any, operationName string) ([]byte, error) {
	target, routed, _, err := my.route(ctx)
	if err != nil {
		return marshalGatewayError(err)
	}
	return target.ExecuteOperation(routed, query, variables, operationName)
}
func (my *TenantGateway) Subscribe(ctx context.Context, query string, variables map[string]any, operationName string) (<-chan []byte, error) {
	target, routed, route, err := my.route(ctx)
	if err != nil {
		return nil, err
	}
	routed = withSubscriptionLease(routed, subscriptionLease{Barrier: my.revocation, Route: route})
	return target.Subscribe(routed, query, variables, operationName)
}
func (my *TenantGateway) EffectiveSchema(ctx context.Context) (SchemaArtifact, error) {
	target, routed, _, err := my.route(ctx)
	if err != nil {
		return SchemaArtifact{}, err
	}
	return target.EffectiveSchema(routed)
}
func (my *TenantGateway) Health(ctx context.Context) (HealthReport, error) {
	if my == nil || len(my.sources) == 0 {
		return HealthReport{}, fmt.Errorf("tenant gateway is unavailable")
	}
	if _, ok := IdentityFromContext(ctx); ok {
		target, routed, route, err := my.route(ctx)
		if err != nil {
			return HealthReport{RouteScoped: true, RouteRevision: my.router.revision}, err
		}
		report, err := target.Health(routed)
		report.RouteScoped, report.RouteRevision = true, route.Revision
		return report, err
	}
	report := HealthReport{SnapshotOK: my.snapshot != nil, RouteRevision: my.router.revision, DatabaseOK: true, CDCConnected: true, CDCState: CDCStateConnected}
	if my.snapshot != nil {
		report.SnapshotFingerprint = my.snapshot.Fingerprint()
	}
	names := slices.Sorted(maps.Keys(my.sources))
	results := collectHealth(names, func(name string) (HealthReport, error) { return my.sources[name].Health(ctx) })
	var failures []error
	ready := 0
	for index, name := range names {
		child, err := results[index].report, results[index].err
		report.SnapshotOK = report.SnapshotOK && child.SnapshotOK
		report.DatabaseOK = report.DatabaseOK && child.DatabaseOK
		mergeHealthCDC(&report, child)
		if child.CDCError != "" {
			if report.CDCError != "" {
				report.CDCError += "; "
			}
			report.CDCError += name + ": " + child.CDCError
		}
		report.SubscriptionAvailable = report.SubscriptionAvailable || child.SubscriptionAvailable
		if child.DatabaseError != "" {
			if report.DatabaseError != "" {
				report.DatabaseError += "; "
			}
			report.DatabaseError += name + ": " + child.DatabaseError
		}
		if err != nil || !child.Ready {
			report.DegradedSources = append(report.DegradedSources, name)
			if err == nil {
				err = fmt.Errorf("not ready")
			}
			failures = append(failures, fmt.Errorf("tenant datasource %s: %w", name, err))
		} else {
			ready++
		}
	}
	report.Degraded = len(failures) != 0
	report.Ready = report.SnapshotOK && ready != 0
	if !report.CDCConfigured {
		report.CDCConnected = false
		report.CDCState = ""
	}
	if report.Ready {
		return report, nil
	}
	return report, errors.Join(failures...)
}
func (my *TenantGateway) SubscriptionAvailable() bool {
	if my == nil || len(my.sources) == 0 {
		return false
	}
	for _, source := range my.sources {
		if !source.SubscriptionAvailable() {
			return false
		}
	}
	return true
}
func marshalGatewayError(err error) ([]byte, error) {
	return marshalReply(context.Background(), gqlReply{Errors: GraphQLErrors(err)})
}

func validateRLSTables(metadata *Metadata, tables []RLSTable) error {
	configured := make(map[string]bool, len(tables))
	for _, table := range tables {
		if table.Protected {
			configured[table.Name] = true
		}
	}
	for _, name := range slices.Sorted(maps.Keys(metadata.Nodes)) {
		class := metadata.Nodes[name]
		if class != nil && name == class.Name && class.Table != "" && !class.Global && !configured[class.Table] {
			return fmt.Errorf("tenant table %s is missing RLS protection", class.Table)
		}
	}
	return nil
}

func validateSharedTenantScope(metadata *Metadata) error {
	for _, name := range slices.Sorted(maps.Keys(metadata.Nodes)) {
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
