package gql

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
)

// NewTenantGateway builds a routing-only Engine over independently built,
// immutable engines. Each datasource keeps its own pool, catalog, plan cache,
// CDC and transaction boundary; the gateway routes before parsing or calling
// any handler. All sources must expose the same GraphQL contract.
type tenantGatewayConfig struct {
	revocation *RevocationBarrier
	rls        map[string]RLSConfig
}
type TenantGatewayOption func(*tenantGatewayConfig)

func WithRevocationBarrier(barrier *RevocationBarrier) TenantGatewayOption {
	return func(config *tenantGatewayConfig) { config.revocation = barrier }
}

// WithRLS validates PostgreSQL role/table facts for shared-table datasources
// and enables transaction-local verified identity on those source engines.
func WithRLS(config map[string]RLSConfig) TenantGatewayOption {
	return func(options *tenantGatewayConfig) { options.rls = config }
}

// TenantGateway is a routing runtime. It deliberately does not embed Engine:
// a gateway has no compiler, database, policy or partially initialized caches.
type TenantGateway struct {
	router     *TenantRouter
	sources    map[string]*Engine
	snapshot   *Snapshot
	revocation *RevocationBarrier
	rls        map[string]RLSConfig
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
	for _, name := range sortedTenantSources(sources) {
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
	if router.mode == TenantModeSharedTable {
		for source, target := range targets {
			rls, configured := config.rls[source]
			production := target.database != nil && target.database.Name() == "postgres" && target.metadata.cfg != nil && !target.metadata.cfg.IsDebug()
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
			if config.rls == nil {
				config.rls = make(map[string]RLSConfig)
			}
			config.rls[source] = rls
		}
	}
	return &TenantGateway{router: router, sources: targets, snapshot: canonical, revocation: config.revocation, rls: config.rls}, nil
}

func (my *TenantGateway) EndpointID() string { return "tenant-gateway" }
func (my *TenantGateway) Path() string       { return "/graphql" }
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
		return nil, ctx, TenantRoute{}, fmt.Errorf("verified identity is required for tenant gateway")
	}
	route, err := my.router.Resolve(ctx, identity)
	if err != nil {
		return nil, ctx, TenantRoute{}, err
	}
	if my.revocation != nil {
		if err := my.revocation.Check(identity, route); err != nil {
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
	return target.Execute(routed, query, variables)
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
func (my *TenantGateway) OperationCost(query, operationName string, variables map[string]any) (uint64, error) {
	if my == nil || len(my.sources) == 0 {
		return 0, fmt.Errorf("tenant gateway is unavailable")
	}
	name := sortedTenantSources(my.sources)[0]
	return my.sources[name].OperationCost(query, operationName, variables)
}
func (my *TenantGateway) Health(ctx context.Context) (HealthReport, error) {
	if my == nil || len(my.sources) == 0 {
		return HealthReport{}, fmt.Errorf("tenant gateway is unavailable")
	}
	report := HealthReport{Ready: true, SnapshotOK: my.snapshot != nil, DatabaseOK: true, CDCConfigured: true, SubscriptionAvailable: true}
	if my.snapshot != nil {
		report.SnapshotFingerprint = my.snapshot.Fingerprint()
	}
	var failures []error
	for _, name := range sortedTenantSources(my.sources) {
		child, err := my.sources[name].Health(ctx)
		report.SnapshotOK = report.SnapshotOK && child.SnapshotOK
		report.DatabaseOK = report.DatabaseOK && child.DatabaseOK
		report.CDCConfigured = report.CDCConfigured && child.CDCConfigured
		report.SubscriptionAvailable = report.SubscriptionAvailable && child.SubscriptionAvailable
		if child.DatabaseError != "" {
			if report.DatabaseError != "" {
				report.DatabaseError += "; "
			}
			report.DatabaseError += name + ": " + child.DatabaseError
		}
		if err != nil || !child.Ready {
			report.Ready = false
			if err == nil {
				err = fmt.Errorf("not ready")
			}
			failures = append(failures, fmt.Errorf("tenant datasource %s: %w", name, err))
		}
	}
	return report, errors.Join(failures...)
}
func (my *TenantGateway) SubscriptionAvailable() bool {
	report, err := my.Health(context.Background())
	return err == nil && report.SubscriptionAvailable
}
func (my *TenantGateway) Close() {}

func marshalGatewayError(err error) ([]byte, error) {
	return marshalSubscriptionReply(gqlReply{Errors: GraphQLErrors(err)}), nil
}

func sortedTenantSources[T any](sources map[string]T) []string {
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func validateRLSTables(metadata *Metadata, tables []RLSTable) error {
	configured := make(map[string]bool, len(tables))
	for _, table := range tables {
		if table.Protected {
			configured[table.Name] = true
		}
	}
	for _, name := range sortedNodeNames(metadata) {
		class := metadata.Nodes[name]
		if class != nil && name == class.Name && class.Table != "" && !class.Global && !configured[class.Table] {
			return fmt.Errorf("tenant table %s is missing RLS protection", class.Table)
		}
	}
	return nil
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
