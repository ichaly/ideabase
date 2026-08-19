package gql

import (
	"bytes"
	"context"
	"crypto/sha256"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ichaly/ideabase/gql/internal/intro"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	validatorrules "github.com/vektah/gqlparser/v2/validator/rules"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

const (
	gatewayMaxTokens        = 100_000
	gatewayMaxDepth         = 8
	gatewayMaxRootFields    = 64
	gatewayMaxFanout        = 32
	gatewayMaxConcurrent    = 4
	gatewayMaxVariableBytes = 1 << 20
	gatewayMaxVariables     = 256
	gatewayMaxChildResponse = 8 << 20
	gatewayTypeKey          = "__gql_gateway_type"
)

// Endpoint 是命名空间组合所需的最小传输无关契约。
type Endpoint interface {
	ExecuteOperation(context.Context, string, map[string]any, string) ([]byte, error)
}

// ComposableEndpoint is the immutable endpoint contract required by a
// namespace. Snapshot is intentionally separate from execution so transports
// can accept operation-only endpoints without exposing a legacy Execute path.
type ComposableEndpoint interface {
	Endpoint
	Snapshot() (*Snapshot, error)
}

// SubscriptionEndpoint is implemented only by endpoints with an executable
// subscription source; its absence removes that namespace from introspection.
type SubscriptionEndpoint interface {
	Subscribe(context.Context, string, map[string]any, string) (<-chan []byte, error)
}

type HealthEndpoint interface {
	Health(context.Context) (HealthReport, error)
}

// EffectiveSchemaEndpoint exposes the identity-aware artifact used for
// introspection. It is deliberately narrower than Endpoint so transports do
// not need to know how a child materializes policy-filtered schemas.
type EffectiveSchemaEndpoint interface {
	EffectiveSchema(context.Context) (SchemaArtifact, error)
}

type EffectiveSchemaCacheIdentityEndpoint interface {
	EffectiveSchemaCacheIdentity(context.Context) (string, bool)
}

type EffectiveSchemaCapability interface{ EffectiveSchemaAvailable() bool }

// Namespace 将一个端点挂载到稳定的公共GraphQL名称下。
type Namespace struct {
	Name       string
	EndpointID string
	Endpoint   ComposableEndpoint
}

// Gateway 组合不可变端点快照，不共享数据库、策略、事务或传输层。
type Gateway struct {
	id       string
	state    atomic.Pointer[gatewayState]
	history  *lruCache[string, *gatewayState]
	refresh  sync.Mutex
	revision uint64
	issued   atomic.Uint64
}

// GatewayUpdate is one monotonic control-plane topology snapshot.
type GatewayUpdate struct {
	Revision   uint64
	Namespaces []Namespace
}

var (
	ErrControlStale    error = controlFailure{"stale control revision", "CONTROL_REVISION_STALE"}
	ErrControlConflict error = controlFailure{"conflicting control revision", "CONTROL_REVISION_CONFLICT"}
)

type controlFailure struct{ message, code string }

func (failure controlFailure) Error() string     { return failure.message }
func (failure controlFailure) ErrorCode() string { return failure.code }

// ControlState is the machine-readable publication state of a gateway.
type ControlState struct {
	Revision    uint64 `json:"revision"`
	Fingerprint string `json:"fingerprint"`
}

type gatewayState struct {
	id                string
	schema            *ast.Schema
	intro             *intro.Handler
	validation        *validatorrules.Rules
	cache             *planCache
	effectiveCache    *lruCache[string, *gatewayState]
	effectiveFlight   singleflight.Group
	buildLimiter      *buildLimiter
	snapshot          *Snapshot
	contracts         map[string]*namespaceContract
	mutations         map[string]*namespaceContract
	topologyIDs       map[string]struct{}
	depth             int
	identityAware     bool
	effectiveArtifact SchemaArtifact
}

type endpointTopology interface {
	topology() (map[string]struct{}, int)
}

// SnapshotEndpoint can bind execution to the exact manifest revision imported
// by a parent Gateway.
type SnapshotEndpoint interface {
	ForSnapshot(string) (ComposableEndpoint, error)
}

type gatewayRevision struct {
	id    string
	state *gatewayState
}

type effectiveNamespace struct {
	contract *namespaceContract
	artifact *SchemaArtifact
	revision string
}

type effectiveArtifactEndpoint struct {
	base         ComposableEndpoint
	snapshot     *Snapshot
	executionPin string
}

type effectiveSubscriptionEndpoint struct{ *effectiveArtifactEndpoint }

func (my *effectiveArtifactEndpoint) EndpointID() string {
	if named, ok := my.base.(interface{ EndpointID() string }); ok {
		return named.EndpointID()
	}
	return "effective-child"
}
func (my *effectiveArtifactEndpoint) Snapshot() (*Snapshot, error) { return my.snapshot, nil }
func (my *effectiveArtifactEndpoint) ExecuteOperation(ctx context.Context, query string, variables map[string]any, operationName string) ([]byte, error) {
	ctx = WithExecutionPin(ctx, my.executionPin)
	return my.base.ExecuteOperation(ctx, query, variables, operationName)
}
func (my *effectiveSubscriptionEndpoint) Subscribe(ctx context.Context, query string, variables map[string]any, operationName string) (<-chan []byte, error) {
	ctx = WithExecutionPin(ctx, my.executionPin)
	return my.base.(SubscriptionEndpoint).Subscribe(ctx, query, variables, operationName)
}

func (my *gatewayState) effective(ctx context.Context) (state *gatewayState, err error) {
	defer func() {
		if err == nil && state != nil {
			err = enforceExecutionPin(ctx, state.effectiveArtifact)
		}
	}()
	revisions := []string{my.snapshot.Fingerprint()}
	names := sortedMapKeys(my.contracts)
	if len(names) > gatewayMaxFanout {
		return nil, gatewayLimitError("namespace effective-schema fan-out")
	}
	cacheParts := make([]string, 0, len(names))
	prefetched := make(map[string]string, len(names))
	cacheable := true
	for _, name := range names {
		contract := my.contracts[name]
		aware, ok := contract.endpoint.(EffectiveSchemaCacheIdentityEndpoint)
		if !ok {
			cacheable = false
			continue
		}
		key, stable := aware.EffectiveSchemaCacheIdentity(ctx)
		if !stable || strings.TrimSpace(key) == "" {
			cacheable = false
			continue
		}
		cacheParts = append(cacheParts, name+"="+key)
		prefetched[name] = key
	}
	cacheKey := ""
	if cacheable {
		identity, _ := IdentityFromContext(ctx)
		route, _ := PinnedRoute(ctx)
		contract, _ := stdjson.Marshal(struct {
			Snapshot                        string
			Tenant, Subject, Actor          string
			Roles                           []string
			Claims                          map[string]any
			PolicyRevision, CredentialEpoch uint64
			Route                           TenantRoute
			Scope                           map[string]any
			Children                        []string
		}{my.snapshot.Fingerprint(), identity.Tenant(), identity.Subject(), identity.Actor(), identity.Roles(), identity.Claims(), identity.PolicyRevision(), identity.CredentialEpoch(), route, Scope(ctx), cacheParts})
		cacheKey = "identity:" + digest(string(contract))
		if cached, ok := my.effectiveCache.Get(cacheKey); ok {
			return cached, nil
		}
	}
	sources := make([]effectiveNamespace, len(names))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(gatewayMaxConcurrent)
	for index, name := range names {
		contract := my.contracts[name]
		sources[index].contract = contract
		endpointSource, ok := contract.endpoint.(EffectiveSchemaEndpoint)
		if capability, declared := contract.endpoint.(EffectiveSchemaCapability); declared && !capability.EffectiveSchemaAvailable() {
			ok = false
		}
		if !ok {
			continue
		}
		index, contract := index, contract
		group.Go(func() error {
			fetch := func(fetchCtx context.Context) (SchemaArtifact, error) {
				return safelyCall(func() (SchemaArtifact, error) { return endpointSource.EffectiveSchema(fetchCtx) })
			}
			var artifact SchemaArtifact
			var err error
			if childKey := prefetched[contract.name]; childKey != "" {
				artifact, err = sharedBuild(groupCtx, &my.effectiveFlight, my.buildLimiter, "child-effective:"+contract.name+"="+childKey, fetch)
			} else {
				artifact, err = fetch(groupCtx)
			}
			if err != nil {
				return fmt.Errorf("namespace %s effective schema: %w", contract.name, err)
			}
			if err := ValidateArtifactIdentity(groupCtx, artifact); err != nil {
				return &publicFailure{"effective schema artifact identity is invalid", "BAD_GATEWAY_RESPONSE"}
			}
			revision, err := effectiveArtifactRevision(artifact)
			if err != nil {
				return &publicFailure{"effective schema artifact is invalid", "BAD_GATEWAY_RESPONSE"}
			}
			sources[index].artifact, sources[index].revision = &artifact, revision
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	for _, source := range sources {
		if source.artifact != nil {
			revisions = append(revisions, source.contract.name+"="+source.revision)
			if cacheKey != "" {
				if artifactKey := strings.TrimSpace(source.artifact.CacheIdentity); artifactKey == "" || artifactKey != prefetched[source.contract.name] {
					cacheKey = ""
				}
			}
		}
	}
	key := strings.Join(revisions, "\x00")
	if cacheKey != "" {
		key = cacheKey
	}
	if cacheable {
		if cached, ok := my.effectiveCache.Get(key); ok {
			return cached, nil
		}
	}
	build := func(context.Context) (*gatewayState, error) {
		if cacheable {
			if cached, ok := my.effectiveCache.Get(key); ok {
				return cached, nil
			}
		}
		namespaces := make([]Namespace, 0, len(sources))
		for _, source := range sources {
			contract := source.contract
			endpoint, composable := source.contract.endpoint.(ComposableEndpoint)
			if !composable {
				return nil, fmt.Errorf("namespace %s endpoint is not composable", contract.name)
			}
			if source.artifact != nil {
				structural := source.artifact.StructuralFingerprint
				if structural == "" {
					baseSnapshot, err := safelyCall(endpoint.Snapshot)
					if err != nil {
						return nil, fmt.Errorf("namespace %s effective artifact snapshot: %w", contract.name, err)
					}
					structural = baseSnapshot.Fingerprint()
				}
				if pinner, ok := endpoint.(SnapshotEndpoint); ok {
					pinned, err := safelyCall(func() (ComposableEndpoint, error) {
						return pinner.ForSnapshot(structural)
					})
					if err != nil {
						return nil, fmt.Errorf("namespace %s effective artifact snapshot: %w", contract.name, err)
					}
					endpoint = pinned
				}
				snapshot, err := snapshotFromEffectiveArtifact(*source.artifact)
				if err != nil {
					return nil, &publicFailure{"effective schema artifact is invalid", "BAD_GATEWAY_RESPONSE"}
				}
				artifactEndpoint := &effectiveArtifactEndpoint{base: endpoint, snapshot: snapshot, executionPin: source.artifact.ExecutionPin}
				if _, ok := endpoint.(SubscriptionEndpoint); ok {
					endpoint = &effectiveSubscriptionEndpoint{artifactEndpoint}
				} else {
					endpoint = artifactEndpoint
				}
			}
			namespaces = append(namespaces, Namespace{Name: contract.name, EndpointID: contract.id, Endpoint: endpoint})
		}
		state, buildErr := buildGatewayState(my.id, namespaces)
		if buildErr == nil {
			state.cache = my.cache
			state.effectiveArtifact = aggregateGatewayArtifact(my.snapshot.Fingerprint(), state.snapshot.Schema(), sources, state.snapshot.Capabilities())
			if cacheable {
				my.effectiveCache.Put(key, state)
			}
		}
		return state, buildErr
	}
	if !cacheable {
		return build(ctx)
	}
	return sharedBuild(ctx, &my.effectiveFlight, my.buildLimiter, key, build)
}

func effectiveArtifactRevision(artifact SchemaArtifact) (string, error) {
	if err := ValidateSchemaArtifact(artifact); err != nil {
		return "", err
	}
	contract, err := stdjson.Marshal(struct {
		Version, Fingerprint, StructuralFingerprint, PermissionSignature, ExecutionPin string
		CacheIdentity                                                                  string
		PolicyRevision, RouteRevision                                                  uint64
		Capabilities                                                                   []Capability
	}{artifact.Version, artifact.Fingerprint, artifact.StructuralFingerprint, artifact.PermissionSignature, artifact.ExecutionPin,
		artifact.CacheIdentity, artifact.PolicyRevision, artifact.RouteRevision, artifact.Capabilities})
	if err != nil {
		return "", err
	}
	return digest(string(contract)), nil
}

func snapshotFromEffectiveArtifact(artifact SchemaArtifact) (*Snapshot, error) {
	if err := ValidateSchemaArtifact(artifact); err != nil {
		return nil, err
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "effective.graphql", Input: artifact.Schema})
	if err != nil {
		return nil, err
	}
	capabilities := completeCapabilities(schema, cloneCapabilities(artifact.Capabilities), HandlerProjection, "endpoint")
	fingerprint, err := artifactFingerprint(artifact.Schema, capabilities, schema)
	if err != nil {
		return nil, err
	}
	snapshot, err := newSnapshotArtifact(artifact.Schema, fingerprint, capabilities, schema)
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (my *gatewayRevision) EndpointID() string           { return my.id }
func (my *gatewayRevision) Snapshot() (*Snapshot, error) { return my.state.snapshot, nil }
func (my *gatewayRevision) Execute(ctx context.Context, query string, variables map[string]any) ([]byte, error) {
	return my.state.execute(ctx, query, variables, "")
}
func (my *gatewayRevision) ExecuteOperation(ctx context.Context, query string, variables map[string]any, operationName string) ([]byte, error) {
	return my.state.execute(ctx, query, variables, operationName)
}
func (my *gatewayRevision) EffectiveSchema(ctx context.Context) (SchemaArtifact, error) {
	return gatewaySchemaArtifact(ctx, my.state)
}
func (my *gatewayRevision) EffectiveSchemaCacheIdentity(ctx context.Context) (string, bool) {
	return gatewayCacheIdentity(ctx, my.state)
}
func (my *gatewayRevision) EffectiveSchemaAvailable() bool {
	return my != nil && my.state != nil && my.state.identityAware
}
func (my *gatewayRevision) Subscribe(ctx context.Context, query string, variables map[string]any, operationName string) (<-chan []byte, error) {
	return my.state.subscribe(ctx, query, variables, operationName)
}
func (my *gatewayRevision) topology() (map[string]struct{}, int) {
	return maps.Clone(my.state.topologyIDs), my.state.depth
}

// NewGateway 冻结确定性的命名空间拓扑与有效Schema。
func NewGateway(id string, namespaces ...Namespace) (*Gateway, error) {
	state, err := buildGatewayState(id, namespaces)
	if err != nil {
		return nil, err
	}
	gateway := &Gateway{id: id, history: newLRU[string, *gatewayState](8)}
	gateway.revision = 0
	gateway.issued.Store(0)
	gateway.state.Store(state)
	return gateway, nil
}

func buildGatewayState(id string, namespaces []Namespace) (*gatewayState, error) {
	if id == "" {
		return nil, fmt.Errorf("gateway缺少稳定endpoint identity")
	}
	namespaces = append([]Namespace(nil), namespaces...)
	for index := range namespaces {
		if pinner, ok := namespaces[index].Endpoint.(SnapshotEndpoint); ok {
			snapshot, err := safelyCall(namespaces[index].Endpoint.Snapshot)
			if err != nil {
				return nil, fmt.Errorf("namespace %s snapshot: %w", namespaces[index].Name, err)
			}
			pinned, err := safelyCall(func() (ComposableEndpoint, error) { return pinner.ForSnapshot(snapshot.Fingerprint()) })
			if err != nil {
				return nil, fmt.Errorf("namespace %s snapshot changed during build: %w", namespaces[index].Name, err)
			}
			namespaces[index].Endpoint = pinned
		}
	}
	seen := map[string]string{id: "gateway"}
	depth := 1
	for _, namespace := range namespaces {
		childID, err := namespaceIdentity(namespace)
		if err != nil {
			return nil, err
		}
		if owner := seen[childID]; owner != "" {
			return nil, fmt.Errorf("endpoint identity重复: %s (%s, %s)", childID, owner, namespace.Name)
		}
		seen[childID] = namespace.Name
		if nested, ok := namespace.Endpoint.(endpointTopology); ok {
			ids, childDepth := nested.topology()
			if _, cycle := ids[id]; cycle {
				return nil, fmt.Errorf("gateway拓扑循环: %s", id)
			}
			for nestedID := range ids {
				if owner := seen[nestedID]; owner != "" && nestedID != childID {
					return nil, fmt.Errorf("endpoint identity重复: %s (%s, %s)", nestedID, owner, namespace.Name)
				}
				seen[nestedID] = namespace.Name
			}
			if childDepth+1 > depth {
				depth = childDepth + 1
			}
		}
	}
	if depth > gatewayMaxDepth {
		return nil, fmt.Errorf("gateway拓扑深度超过%d", gatewayMaxDepth)
	}
	schema, source, contracts, capabilities, err := composeNamespaces(namespaces)
	if err != nil {
		return nil, err
	}
	for _, contract := range contracts {
		if declared, ok := contract.endpoint.(interface{ HealthAvailable() bool }); ok && declared.HealthAvailable() {
			if _, implements := contract.endpoint.(HealthEndpoint); !implements {
				return nil, fmt.Errorf("namespace %s declares HealthAvailable but lacks HealthEndpoint", contract.name)
			}
		}
	}
	capabilities = completeCapabilities(schema, capabilities, HandlerRemote, "namespace")
	mutations := make(map[string]*namespaceContract)
	effective := false
	for _, contract := range contracts {
		_, effectiveChild := contract.endpoint.(EffectiveSchemaEndpoint)
		if capability, ok := contract.endpoint.(EffectiveSchemaCapability); ok {
			effectiveChild = capability.EffectiveSchemaAvailable()
		}
		effective = effective || effectiveChild
		for field := range contract.mutations {
			mutations[field] = contract
		}
	}
	if effective {
		for _, contract := range contracts {
			_, hasEffectiveEndpoint := contract.endpoint.(EffectiveSchemaEndpoint)
			available := hasEffectiveEndpoint
			if capability, declared := contract.endpoint.(EffectiveSchemaCapability); declared {
				if capability.EffectiveSchemaAvailable() && !hasEffectiveEndpoint {
					return nil, fmt.Errorf("namespace %s declares EffectiveSchemaCapability but lacks EffectiveSchemaEndpoint", contract.name)
				}
				available = hasEffectiveEndpoint && capability.EffectiveSchemaAvailable()
			}
			if !available {
				if _, explicitlyStatic := contract.endpoint.(EffectiveSchemaCapability); explicitlyStatic {
					continue
				}
				return nil, fmt.Errorf("namespace %s lacks identity-aware effective schema; static endpoints must explicitly declare mode", contract.name)
			}
		}
	}
	identity := []string{id, source}
	for _, namespace := range namespaces {
		snapshot, snapshotErr := safelyCall(namespace.Endpoint.Snapshot)
		if snapshotErr != nil {
			return nil, fmt.Errorf("namespace %s snapshot: %w", namespace.Name, snapshotErr)
		}
		childID, _ := namespaceIdentity(namespace)
		identity = append(identity, namespace.Name+"\x00"+childID+"\x00"+snapshot.Fingerprint())
	}
	sort.Strings(identity[2:])
	fingerprint := sha256.Sum256([]byte(strings.Join(identity, "\x01")))
	index := make(map[string]Capability, len(capabilities))
	for _, capability := range capabilities {
		index[capability.Coordinate] = capability
	}
	snapshot := &Snapshot{schema: source, fingerprint: fmt.Sprintf("%x", fingerprint), capabilities: capabilities, index: index}
	state := &gatewayState{
		id: id, schema: schema, intro: intro.New(schema), validation: validatorrules.NewDefaultRules(), snapshot: snapshot,
		cache: newPlanCache(planCacheSize), contracts: contracts, mutations: mutations, topologyIDs: identitySet(seen), depth: depth, identityAware: effective,
		buildLimiter: newBuildLimiter(16, 4),
	}
	if effective {
		state.effectiveCache = newLRU[string, *gatewayState](128)
	}
	return state, nil
}

func identitySet(names map[string]string) map[string]struct{} {
	ids := make(map[string]struct{}, len(names))
	for id := range names {
		ids[id] = struct{}{}
	}
	return ids
}

func namespaceIdentity(namespace Namespace) (string, error) {
	if namespace.Endpoint == nil {
		return "", fmt.Errorf("namespace %s缺少endpoint", namespace.Name)
	}
	if namespace.EndpointID != "" {
		return namespace.EndpointID, nil
	}
	if named, ok := namespace.Endpoint.(interface{ EndpointID() string }); ok && named.EndpointID() != "" {
		return named.EndpointID(), nil
	}
	return "", fmt.Errorf("namespace %s缺少稳定endpoint identity", namespace.Name)
}

func (my *Gateway) EndpointID() string { return my.id }
func (my *Gateway) ManifestRevision() uint64 {
	_, revision, _ := my.ManifestState()
	return revision
}
func (my *Gateway) ControlState() ControlState {
	snapshot, revision, _ := my.ManifestState()
	if snapshot == nil {
		return ControlState{}
	}
	return ControlState{Revision: revision, Fingerprint: snapshot.Fingerprint()}
}

// ManifestState returns one atomic control-plane view for transport publication.
func (my *Gateway) ManifestState() (*Snapshot, uint64, error) {
	if my == nil {
		return nil, 0, fmt.Errorf("gateway不可用")
	}
	my.refresh.Lock()
	defer my.refresh.Unlock()
	state := my.state.Load()
	if state == nil {
		return nil, my.revision, fmt.Errorf("gateway不可用")
	}
	return state.snapshot, my.revision, nil
}
func (my *Gateway) EffectiveSchemaAvailable() bool {
	state := my.current()
	return state != nil && state.identityAware
}

func (my *Gateway) ForSnapshot(fingerprint string) (ComposableEndpoint, error) {
	state := my.current()
	if state != nil && state.snapshot.Fingerprint() == fingerprint {
		return &gatewayRevision{id: my.id, state: state}, nil
	}
	if my != nil && my.history != nil {
		if state, ok := my.history.Get(fingerprint); ok {
			return &gatewayRevision{id: my.id, state: state}, nil
		}
	}
	return nil, fmt.Errorf("snapshot revision unavailable")
}

func (my *Gateway) topology() (map[string]struct{}, int) {
	state := my.current()
	if state == nil {
		return nil, 0
	}
	return maps.Clone(state.topologyIDs), state.depth
}

func (my *Gateway) Snapshot() (*Snapshot, error) {
	state := my.current()
	if state == nil || state.snapshot == nil {
		return nil, fmt.Errorf("gateway snapshot不可用")
	}
	return state.snapshot, nil
}

// EffectiveSchema composes the exact identity-specific child artifacts, so
// namespace gateways remain policy-aware at every nesting depth.
func (my *Gateway) EffectiveSchema(ctx context.Context) (SchemaArtifact, error) {
	return gatewaySchemaArtifact(ctx, my.current())
}

func (my *Gateway) EffectiveSchemaCacheIdentity(ctx context.Context) (string, bool) {
	return gatewayCacheIdentity(ctx, my.current())
}

func gatewayCacheIdentity(ctx context.Context, state *gatewayState) (string, bool) {
	if state == nil || !state.identityAware {
		return "", false
	}
	parts := make([]string, 0, len(state.contracts))
	for _, name := range sortedMapKeys(state.contracts) {
		contract := state.contracts[name]
		aware, ok := contract.endpoint.(EffectiveSchemaCacheIdentityEndpoint)
		if !ok {
			return "", false
		}
		key, stable := aware.EffectiveSchemaCacheIdentity(ctx)
		if !stable || strings.TrimSpace(key) == "" {
			return "", false
		}
		parts = append(parts, name+"="+key)
	}
	return digest(strings.Join(parts, "\x00")), true
}

func gatewaySchemaArtifact(ctx context.Context, state *gatewayState) (SchemaArtifact, error) {
	if state == nil {
		return SchemaArtifact{}, fmt.Errorf("gateway不可用")
	}
	structural := state.snapshot.Fingerprint()
	if state.identityAware {
		var err error
		if state, err = state.effective(ctx); err != nil {
			return SchemaArtifact{}, err
		}
	}
	schema := state.snapshot.Schema()
	artifact := state.effectiveArtifact
	if artifact.Schema == "" {
		artifact = SchemaArtifact{Schema: schema, Fingerprint: digest(schema), StructuralFingerprint: structural,
			PermissionSignature: state.snapshot.Fingerprint(), Capabilities: state.snapshot.Capabilities()}
	}
	artifact.Schema, artifact.Fingerprint = schema, digest(schema)
	if identity, ok := IdentityFromContext(ctx); ok {
		artifact.Identity = IdentityDescriptorFromIdentity(identity)
	}
	return FinalizeSchemaArtifact(artifact), nil
}

func aggregateGatewayArtifact(structural, schema string, sources []effectiveNamespace, capabilities []Capability) SchemaArtifact {
	permissions := make([]string, 0, len(sources))
	cacheIdentities := make([]string, 0, len(sources))
	cacheable := true
	var policyRevision, routeRevision uint64
	for _, source := range sources {
		if source.artifact == nil {
			continue
		}
		permissions = append(permissions, source.artifact.PermissionSignature)
		if strings.TrimSpace(source.artifact.CacheIdentity) == "" {
			cacheable = false
		} else {
			cacheIdentities = append(cacheIdentities, source.contract.name+"="+source.artifact.CacheIdentity)
		}
		if source.artifact.PolicyRevision > policyRevision {
			policyRevision = source.artifact.PolicyRevision
		}
		if source.artifact.RouteRevision > routeRevision {
			routeRevision = source.artifact.RouteRevision
		}
	}
	artifact := SchemaArtifact{Schema: schema, Fingerprint: digest(schema),
		StructuralFingerprint: structural, PermissionSignature: digest(strings.Join(permissions, "\x00")),
		PolicyRevision: policyRevision, RouteRevision: routeRevision, Capabilities: cloneCapabilities(capabilities)}
	if cacheable && len(cacheIdentities) > 0 {
		sort.Strings(cacheIdentities)
		artifact.CacheIdentity = digest(strings.Join(cacheIdentities, "\x00"))
	}
	return FinalizeSchemaArtifact(artifact)
}

func (my *Gateway) Schema() (string, error) {
	snapshot, err := my.Snapshot()
	if err != nil {
		return "", err
	}
	return snapshot.Schema(), nil
}

func (my *Gateway) current() *gatewayState {
	if my == nil {
		return nil
	}
	return my.state.Load()
}

// Apply atomically publishes a monotonic, idempotent control-plane update.
func (my *Gateway) Apply(update GatewayUpdate) (*Snapshot, error) {
	if my == nil {
		return nil, fmt.Errorf("gateway不可用")
	}
	if update.Revision == 0 {
		return nil, fmt.Errorf("gateway revision is required")
	}
	my.refresh.Lock()
	stale := update.Revision < my.revision
	my.refresh.Unlock()
	if stale {
		return nil, fmt.Errorf("%w: %d", ErrControlStale, update.Revision)
	}
	state, err := buildGatewayState(my.id, update.Namespaces)
	if err != nil {
		return nil, err
	}
	my.refresh.Lock()
	defer my.refresh.Unlock()
	current := my.state.Load()
	if update.Revision < my.revision {
		return nil, fmt.Errorf("%w: %d", ErrControlStale, update.Revision)
	}
	if update.Revision == my.revision {
		if current != nil && current.snapshot.Fingerprint() == state.snapshot.Fingerprint() {
			return current.snapshot, nil
		}
		return nil, fmt.Errorf("%w: %d", ErrControlConflict, update.Revision)
	}
	for issued := my.issued.Load(); issued < update.Revision && !my.issued.CompareAndSwap(issued, update.Revision); issued = my.issued.Load() {
	}
	if current != nil {
		my.history.Put(current.snapshot.Fingerprint(), current)
	}
	my.revision = update.Revision
	my.state.Store(state)
	return state.snapshot, nil
}

// Refresh is the local monotonic update path.
func (my *Gateway) Refresh(namespaces ...Namespace) error {
	if my == nil {
		return fmt.Errorf("gateway不可用")
	}
	_, err := my.Apply(GatewayUpdate{Revision: my.issued.Add(1), Namespaces: namespaces})
	return err
}

func (my *Gateway) Capabilities() ([]Capability, error) {
	snapshot, err := my.Snapshot()
	if err != nil {
		return nil, err
	}
	return snapshot.Capabilities(), nil
}

// SubscriptionAvailable reports whether the current composed snapshot exposes
// at least one executable namespace subscription.
func (my *Gateway) SubscriptionAvailable() bool {
	state := my.current()
	return state != nil && state.schema.Subscription != nil
}

func (my *Gateway) HealthAvailable() bool {
	return healthAvailable(my.current())
}

func healthAvailable(state *gatewayState) bool {
	if state == nil {
		return false
	}
	for _, contract := range state.contracts {
		if source, ok := contract.endpoint.(interface{ HealthAvailable() bool }); ok {
			if !source.HealthAvailable() {
				return false
			}
		}
		if _, ok := contract.endpoint.(HealthEndpoint); !ok {
			return false
		}
	}
	return true
}

func (my *Gateway) Health(ctx context.Context) (HealthReport, error) {
	state, revision := my.manifestState()
	if state == nil || !healthAvailable(state) {
		return HealthReport{}, fmt.Errorf("gateway health unavailable")
	}
	report := HealthReport{Ready: true, SnapshotOK: true, DatabaseOK: true, CDCConnected: true, CDCState: CDCStateConnected, SnapshotFingerprint: state.snapshot.Fingerprint(), ControlRevision: revision, SubscriptionAvailable: state.schema.Subscription != nil}
	names := sortedMapKeys(state.contracts)
	results := collectHealth(names, func(name string) (HealthReport, error) {
		contract := state.contracts[name]
		return safelyCall(func() (HealthReport, error) { return contract.endpoint.(HealthEndpoint).Health(ctx) })
	})
	var failures []error
	for index, name := range names {
		contract := state.contracts[name]
		child, err := results[index].report, results[index].err
		if child.Degraded {
			report.Degraded = true
			for _, source := range child.DegradedSources {
				report.DegradedSources = append(report.DegradedSources, contract.name+":"+source)
			}
		}
		report.SnapshotOK = report.SnapshotOK && child.SnapshotOK
		report.DatabaseOK = report.DatabaseOK && child.DatabaseOK
		mergeHealthCDC(&report, child)
		if child.CDCError != "" {
			if report.CDCError != "" {
				report.CDCError += "; "
			}
			report.CDCError += contract.name + ": " + child.CDCError
		}
		if child.DatabaseError != "" {
			if report.DatabaseError != "" {
				report.DatabaseError += "; "
			}
			report.DatabaseError += contract.name + ": " + child.DatabaseError
		}
		if err != nil || !child.Ready {
			report.Ready = false
			if err == nil {
				err = fmt.Errorf("not ready")
			}
			failures = append(failures, fmt.Errorf("namespace %s: %w", contract.name, err))
		}
	}
	if !report.CDCConfigured {
		report.CDCConnected = false
		report.CDCState = ""
	}
	return report, errors.Join(failures...)
}

func (my *Gateway) manifestState() (*gatewayState, uint64) {
	if my == nil {
		return nil, 0
	}
	my.refresh.Lock()
	defer my.refresh.Unlock()
	return my.state.Load(), my.revision
}

// Subscribe validates the public single-root subscription, then forwards one
// child stream while preserving the namespace response and error paths.
func (my *Gateway) Subscribe(ctx context.Context, query string, variables map[string]any, operationName string) (<-chan []byte, error) {
	if my == nil {
		return nil, fmt.Errorf("gateway不可用")
	}
	state := my.current()
	if state == nil {
		return nil, fmt.Errorf("gateway subscription不可用")
	}
	return state.subscribe(ctx, query, variables, operationName)
}

func (my *gatewayState) subscribe(ctx context.Context, query string, variables map[string]any, operationName string) (<-chan []byte, error) {
	if my != nil && my.identityAware {
		effective, err := my.effective(ctx)
		if err != nil {
			return nil, err
		}
		return effective.subscribe(ctx, query, variables, operationName)
	}
	if my == nil || my.schema.Subscription == nil {
		return nil, fmt.Errorf("gateway subscription不可用")
	}
	if errs := validateGatewayRequest(query, variables); len(errs) > 0 {
		return nil, errs
	}
	operation, coerced, err := my.prepareOperation(query, variables, operationName)
	if err != nil {
		return nil, err
	}
	if operation.Operation != ast.Subscription {
		return nil, fmt.Errorf("未找到可执行的subscription操作")
	}
	if len(operation.SelectionSet) != 1 {
		return nil, fmt.Errorf("subscription必须且只能选择一个namespace根字段")
	}
	field, ok := operation.SelectionSet[0].(*ast.Field)
	if !ok || len(field.SelectionSet) != 1 {
		return nil, fmt.Errorf("subscription namespace必须且只能选择一个child根字段")
	}
	contract := my.contracts[field.Name]
	subscriber, ok := contractEndpointSubscription(contract)
	if !ok {
		return nil, fmt.Errorf("namespace %s subscription不可用", field.Name)
	}
	childQuery, childVariables := childOperation(ast.Subscription, field.SelectionSet, operation.VariableDefinitions, coerced, contract.publicToChild)
	source, err := subscribeChild(ctx, subscriber, childQuery, childVariables)
	if err != nil {
		return nil, err
	}
	events := make(chan []byte, 1)
	go my.forwardSubscription(ctx, events, source, field, contract)
	return events, nil
}

func contractEndpointSubscription(contract *namespaceContract) (SubscriptionEndpoint, bool) {
	if contract == nil || !contract.subscription {
		return nil, false
	}
	subscriber, ok := contract.endpoint.(SubscriptionEndpoint)
	return subscriber, ok
}

func (my *gatewayState) forwardSubscription(ctx context.Context, events chan<- []byte, source <-chan []byte, field *ast.Field, contract *namespaceContract) {
	defer close(events)
	for {
		select {
		case <-ctx.Done():
			return
		case body, ok := <-source:
			if !ok {
				return
			}
			child, err := decodeEndpointReply(body)
			if err != nil {
				frame, _ := marshalReply(ctx, gqlReply{Data: map[string]any{responseKey(field): nil}, Errors: endpointProtocolError(responseKey(field), field)})
				select {
				case events <- frame:
				case <-ctx.Done():
				}
				return
			}
			definition := my.schema.Subscription.Fields.ForName(field.Name)
			value, completionErrors, _ := completeEndpointValue(my.schema, child.Data, definition.Type, field.SelectionSet, nil, child.Errors, contract)
			prefixErrors(child.Errors, responseKey(field), field)
			prefixLocalErrors(completionErrors, responseKey(field), field)
			child.Errors = append(child.Errors, completionErrors...)
			frame, err := marshalReply(ctx, gqlReply{Data: map[string]any{responseKey(field): value}, Errors: child.Errors})
			if err != nil {
				if frame, fallbackErr := marshalReply(ctx, gqlReply{Errors: GraphQLErrors(err)}); fallbackErr == nil {
					select {
					case events <- frame:
					case <-ctx.Done():
					}
				}
				return
			}
			select {
			case events <- frame:
			case <-ctx.Done():
				return
			}
		}
	}
}

// Execute 对组合Schema只校验一次，并将每个命名空间选择集作为一个子操作委派。
func (my *Gateway) Execute(ctx context.Context, query string, variables map[string]any) ([]byte, error) {
	return my.ExecuteOperation(ctx, query, variables, "")
}

// ExecuteOperation executes one named operation from a multi-operation document.
func (my *Gateway) ExecuteOperation(ctx context.Context, query string, variables map[string]any, operationName string) ([]byte, error) {
	state := my.current()
	if state == nil {
		return nil, fmt.Errorf("gateway不可用")
	}
	return state.execute(ctx, query, variables, operationName)
}

func (my *gatewayState) execute(ctx context.Context, query string, variables map[string]any, operationName string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if my.identityAware {
		effective, err := my.effective(ctx)
		if err != nil {
			return marshalReply(ctx, gqlReply{Errors: GraphQLErrors(err)})
		}
		return effective.execute(ctx, query, variables, operationName)
	}
	if errs := validateGatewayRequest(query, variables); len(errs) > 0 {
		return marshalReply(ctx, gqlReply{Errors: errs})
	}
	operation, coerced, err := my.prepareOperation(query, variables, operationName)
	if err != nil {
		return marshalReply(ctx, gqlReply{Errors: GraphQLErrors(err)})
	}
	dataOperation, introspection := splitIntrospection(operation)
	data := make(map[string]any)
	var executed gqlReply
	if dataOperation != nil {
		switch dataOperation.Operation {
		case ast.Query:
			executed = my.executeQuery(ctx, dataOperation, coerced)
		case ast.Mutation:
			executed = my.executeMutation(ctx, dataOperation, coerced)
		default:
			executed.Errors = gqlerror.List{gqlerror.Errorf("gateway暂不支持%s操作", dataOperation.Operation)}
		}
		if executed.Data == nil && len(executed.Errors) > 0 {
			return marshalReply(ctx, executed)
		}
		if !executed.nullData {
			for key, value := range executed.Data {
				data[key] = value
			}
		}
	}
	if introspection != nil {
		projected, introErr := my.intro.Introspect(introspection, coerced)
		if introErr != nil {
			executed.Errors = append(executed.Errors, GraphQLErrors(introErr)...)
		} else {
			for key, value := range projected {
				data[key] = value
			}
		}
	}
	executed.Data = data
	return marshalReply(ctx, executed)
}

func (my *gatewayState) prepareOperation(query string, variables map[string]any, operationName string) (*ast.OperationDefinition, map[string]any, error) {
	key := planKeyFor(operationName, query, "", my.snapshot.Fingerprint())
	conditional := hasConditionalDirective(query)
	if !conditional {
		if entry, ok := my.cache.Get(key); ok {
			coerced, err := coerceVariables(my.schema, entry.operation, cloneVariables(variables))
			return entry.operation, coerced, err
		}
	}
	prepared, err := prepareFrontend(my.schema, query, operationName, cloneVariables(variables), gatewayMaxTokens, my.validation)
	if err != nil {
		return nil, nil, err
	}
	if len(prepared.operation.SelectionSet) > gatewayMaxRootFields {
		return nil, nil, gatewayLimitError("root field")
	}
	if !conditional {
		my.cache.Put(key, &planEntry{operation: prepared.operation})
	}
	return prepared.operation, prepared.variables, nil
}

func (my *gatewayState) executeQuery(ctx context.Context, operation *ast.OperationDefinition, variables map[string]any) gqlReply {
	reply := gqlReply{Data: make(map[string]any)}
	results := make([]queryResult, len(operation.SelectionSet))
	groups := make(map[*namespaceContract][]queryEntry)
	var group errgroup.Group
	for index, selection := range operation.SelectionSet {
		field, ok := selection.(*ast.Field)
		if !ok {
			continue
		}
		if field.Name == "__typename" {
			results[index] = queryResult{key: responseKey(field), value: "Query"}
			continue
		}
		contract := my.contracts[field.Name]
		if contract == nil {
			continue
		}
		groups[contract] = append(groups[contract], queryEntry{index: index, field: field})
	}
	if len(groups) > gatewayMaxFanout {
		return gqlReply{Errors: gatewayLimitError("namespace fan-out")}
	}
	if len(groups) == 1 {
		for contract, entries := range groups {
			my.executeQueryGroup(ctx, operation, variables, contract, entries, results)
		}
	} else {
		group.SetLimit(gatewayMaxConcurrent)
		for contract, entries := range groups {
			group.Go(func() error {
				my.executeQueryGroup(ctx, operation, variables, contract, entries, results)
				return nil
			})
		}
		_ = group.Wait()
	}
	for _, result := range results {
		if result.key == "" {
			continue
		}
		reply.Data[result.key] = result.value
		reply.Errors = append(reply.Errors, result.errors...)
	}
	return reply
}

type queryEntry struct {
	index int
	field *ast.Field
}

type queryResult struct {
	key    string
	value  any
	errors gqlerror.List
}

type queryAlias struct {
	entry int
	key   string
}

func (my *gatewayState) executeQueryGroup(ctx context.Context, operation *ast.OperationDefinition, variables map[string]any, contract *namespaceContract, entries []queryEntry, results []queryResult) {
	if err := ctx.Err(); err != nil {
		for _, entry := range entries {
			key := responseKey(entry.field)
			results[entry.index] = queryResult{key: key, errors: endpointError(key, entry.field, err)}
		}
		return
	}
	set, aliases := groupedSelection(entries)
	query, childVariables := childOperation(ast.Query, set, operation.VariableDefinitions, variables, contract.publicToChild)
	body, err := executeChild(ctx, contract.endpoint, query, childVariables)
	if err != nil {
		for _, entry := range entries {
			key := responseKey(entry.field)
			results[entry.index] = queryResult{key: key, errors: endpointError(key, entry.field, err)}
		}
		return
	}
	if !consumeResponseBudget(ctx, len(body)) {
		for _, entry := range entries {
			key := responseKey(entry.field)
			results[entry.index] = queryResult{key: key, errors: gatewayLimitError("response")}
		}
		return
	}
	child, err := decodeEndpointReply(body)
	if err != nil {
		for _, entry := range entries {
			key := responseKey(entry.field)
			results[entry.index] = queryResult{key: key, errors: endpointProtocolError(key, entry.field)}
		}
		return
	}
	data := make([]map[string]any, len(entries))
	errors := make([]gqlerror.List, len(entries))
	for index := range data {
		data[index] = make(map[string]any)
	}
	for synthetic, alias := range aliases {
		if value, ok := child.Data[synthetic]; ok {
			data[alias.entry][alias.key] = value
		}
	}
	for _, childErr := range child.Errors {
		copy := cloneGraphQLError(childErr)
		name, ok := firstPathName(copy.Path)
		alias, mapped := aliases[name]
		if !ok || !mapped {
			for index := range errors {
				errors[index] = append(errors[index], gqlerror.Errorf("endpoint returned an unmappable error"))
			}
			continue
		}
		copy.Path[0] = ast.PathName(alias.key)
		errors[alias.entry] = append(errors[alias.entry], copy)
	}
	for index, entry := range entries {
		key := responseKey(entry.field)
		definition := my.schema.Query.Fields.ForName(entry.field.Name)
		value, completionErrors, _ := completeEndpointValue(my.schema, data[index], definition.Type, entry.field.SelectionSet, nil, errors[index], contract)
		prefixErrors(errors[index], key, entry.field)
		prefixLocalErrors(completionErrors, key, entry.field)
		errors[index] = append(errors[index], completionErrors...)
		results[entry.index] = queryResult{key: key, value: value, errors: errors[index]}
	}
}

func groupedSelection(entries []queryEntry) (ast.SelectionSet, map[string]queryAlias) {
	aliases := make(map[string]queryAlias)
	if len(entries) == 1 {
		for _, selection := range entries[0].field.SelectionSet {
			if field, ok := selection.(*ast.Field); ok {
				key := responseKey(field)
				aliases[key] = queryAlias{key: key}
			}
		}
		return entries[0].field.SelectionSet, aliases
	}
	var set ast.SelectionSet
	for entryIndex, entry := range entries {
		for fieldIndex, selection := range entry.field.SelectionSet {
			field, ok := selection.(*ast.Field)
			if !ok {
				continue
			}
			clone := *field
			synthetic := fmt.Sprintf("_g%d_%d", entryIndex, fieldIndex)
			clone.Alias = synthetic
			set = append(set, &clone)
			aliases[synthetic] = queryAlias{entry: entryIndex, key: responseKey(field)}
		}
	}
	return set, aliases
}

func firstPathName(path ast.Path) (string, bool) {
	if len(path) == 0 {
		return "", false
	}
	name, ok := path[0].(ast.PathName)
	return string(name), ok
}

func cloneGraphQLError(err *gqlerror.Error) *gqlerror.Error {
	copy := *err
	copy.Path = append(ast.Path(nil), err.Path...)
	copy.Locations = append([]gqlerror.Location(nil), err.Locations...)
	copy.Extensions = maps.Clone(err.Extensions)
	return &copy
}

func (my *gatewayState) executeMutation(ctx context.Context, operation *ast.OperationDefinition, variables map[string]any) gqlReply {
	reply := gqlReply{Data: make(map[string]any)}
	committed, committedSet := make([]string, 0), make(map[string]bool)
	indeterminate, indeterminateSet := make([]string, 0), make(map[string]bool)
	for _, selection := range operation.SelectionSet {
		field, ok := selection.(*ast.Field)
		if !ok {
			continue
		}
		if field.Name == "__typename" {
			reply.Data[responseKey(field)] = "Mutation"
			continue
		}
		contract := my.mutations[field.Name]
		if contract == nil {
			continue
		}
		definition := my.schema.Mutation.Fields.ForName(field.Name)
		key := responseKey(field)
		childField := *field
		childField.Name = contract.mutations[field.Name]
		childField.Alias = key
		query, childVariables := childOperation(ast.Mutation, ast.SelectionSet{&childField}, operation.VariableDefinitions, variables, contract.publicToChild)
		body, err := executeChild(WithMutationCommitMetadata(ctx), contract.endpoint, query, childVariables)
		var child endpointReply
		uncertain := err != nil
		responseLimited := false
		if err == nil && !consumeResponseBudget(ctx, len(body)) {
			err = errResponseLimit
			responseLimited = true
		}
		if err == nil {
			child, err = decodeEndpointReply(body)
			uncertain = err != nil
		}
		if err != nil {
			if errors.Is(err, errResponseLimit) {
				responseLimited = true
				uncertain = false
				child.Errors = gatewayLimitError("response")
			} else {
				child.Errors = indeterminateCommitError(key, field)
			}
		} else {
			if child.Data == nil {
				child.Data = make(map[string]any)
			}
			completed, completionErrors, _ := completeEndpointValue(my.schema, child.Data[key], definition.Type, field.SelectionSet, ast.Path{ast.PathName(key)}, child.Errors, contract)
			child.Data[key] = completed
			prefixErrors(child.Errors, "", field)
			prefixLocalErrors(completionErrors, "", field)
			child.Errors = append(child.Errors, completionErrors...)
		}
		value := child.Data[key]
		reply.Data[key] = value
		reply.Errors = append(reply.Errors, child.Errors...)
		commitState, _ := child.Extensions["mutationCommit"].(string)
		if responseLimited || commitState == "indeterminate" || (commitState == "" && len(child.Errors) > 0) {
			uncertain = true
		}
		if uncertain {
			if !responseLimited {
				markIndeterminateCommit(child.Errors)
			}
			if !indeterminateSet[contract.name] {
				indeterminateSet[contract.name] = true
				indeterminate = append(indeterminate, contract.name)
			}
		} else if commitState != "rolled_back" && !committedSet[contract.name] {
			committedSet[contract.name] = true
			committed = append(committed, contract.name)
		}
		if len(child.Errors) > 0 && definition != nil && definition.Type.NonNull && value == nil {
			reply.nullData = true
			break
		}
	}
	if len(reply.Errors) > 0 {
		reply.Extensions = map[string]any{"committedNamespaces": committed}
		if len(indeterminate) > 0 {
			reply.Extensions["indeterminateNamespaces"] = indeterminate
		}
	}
	return reply
}

type endpointReply struct {
	Data       map[string]any `json:"data"`
	Errors     gqlerror.List  `json:"errors"`
	Extensions map[string]any `json:"extensions"`
}

func decodeEndpointReply(body []byte) (endpointReply, error) {
	var reply endpointReply
	if len(body) > gatewayMaxChildResponse {
		return reply, fmt.Errorf("endpoint response exceeds %d bytes", gatewayMaxChildResponse)
	}
	decoder := stdjson.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&reply); err != nil {
		return reply, fmt.Errorf("endpoint返回无效GraphQL JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return reply, fmt.Errorf("endpoint返回多个JSON值")
	} else if !errors.Is(err, io.EOF) {
		return reply, fmt.Errorf("endpoint返回无效尾部JSON: %w", err)
	}
	if reply.Data == nil && len(reply.Errors) == 0 {
		return reply, fmt.Errorf("endpoint响应缺少data或errors")
	}
	for _, item := range reply.Errors {
		if item == nil || strings.TrimSpace(item.Message) == "" {
			return reply, fmt.Errorf("endpoint响应包含无效GraphQL error")
		}
	}
	return reply, nil
}

func gatewayLimitError(resource string) gqlerror.List {
	err := gqlerror.Errorf("gateway %s limit exceeded", resource)
	err.Extensions = map[string]any{"code": "LIMIT_EXCEEDED"}
	return gqlerror.List{err}
}

func validateGatewayRequest(query string, variables map[string]any) gqlerror.List {
	if len(query) > maxDocumentBytes {
		return gatewayLimitError("document size")
	}
	if len(variables) > gatewayMaxVariables {
		return gatewayLimitError("variable count")
	}
	if len(variables) == 0 {
		return nil
	}
	body, err := stdjson.Marshal(variables)
	if err != nil {
		item := gqlerror.Errorf("gateway variables must be JSON-compatible")
		item.Extensions = map[string]any{"code": "BAD_USER_INPUT"}
		return gqlerror.List{item}
	}
	if len(body) > gatewayMaxVariableBytes {
		return gatewayLimitError("variable size")
	}
	return nil
}

func childOperation(kind ast.Operation, set ast.SelectionSet, definitions ast.VariableDefinitionList, variables map[string]any, reverse map[string]string) (string, map[string]any) {
	used := make(map[string]bool)
	collectVariables(set, used)
	var out strings.Builder
	out.WriteString(string(kind))
	childVariables := make(map[string]any, len(used))
	first := true
	for _, definition := range definitions {
		if !used[definition.Variable] {
			continue
		}
		if first {
			out.WriteString("(")
			first = false
		} else {
			out.WriteString(", ")
		}
		out.WriteString("$")
		out.WriteString(definition.Variable)
		out.WriteString(": ")
		writeChildType(&out, definition.Type, reverse)
		if value, ok := variables[definition.Variable]; ok {
			childVariables[definition.Variable] = cloneVariableValue(value)
		}
	}
	if !first {
		out.WriteString(")")
	}
	out.WriteString(" ")
	writeGatewaySelectionSet(&out, set, reverse)
	return out.String(), childVariables
}

func writeGatewaySelectionSet(out *strings.Builder, set ast.SelectionSet, reverse map[string]string) {
	out.WriteString("{")
	for _, selection := range set {
		switch node := selection.(type) {
		case *ast.Field:
			out.WriteString(" ")
			if node.Alias != "" && node.Alias != node.Name {
				out.WriteString(node.Alias)
				out.WriteString(": ")
			}
			out.WriteString(node.Name)
			if len(node.Arguments) > 0 {
				out.WriteString("(")
				for index, argument := range node.Arguments {
					if index > 0 {
						out.WriteString(", ")
					}
					out.WriteString(argument.Name)
					out.WriteString(": ")
					out.WriteString(argument.Value.String())
				}
				out.WriteString(")")
			}
			if len(node.SelectionSet) > 0 {
				out.WriteString(" ")
				writeGatewaySelectionSet(out, node.SelectionSet, reverse)
			}
		case *ast.InlineFragment:
			out.WriteString(" ...")
			if node.TypeCondition != "" {
				out.WriteString(" on ")
				if child := reverse[node.TypeCondition]; child != "" {
					out.WriteString(child)
				} else {
					out.WriteString(node.TypeCondition)
				}
			}
			out.WriteString(" ")
			writeGatewaySelectionSet(out, node.SelectionSet, reverse)
		}
	}
	if hasInlineFragment(set) {
		out.WriteString(" ")
		out.WriteString(gatewayTypeAlias(set))
		out.WriteString(": __typename")
	}
	out.WriteString(" }")
}

func hasInlineFragment(set ast.SelectionSet) bool {
	for _, selection := range set {
		if _, ok := selection.(*ast.InlineFragment); ok {
			return true
		}
	}
	return false
}

func gatewayTypeAlias(set ast.SelectionSet) string {
	for alias := gatewayTypeKey; ; alias += "_" {
		conflict := false
		for _, selection := range set {
			field, ok := selection.(*ast.Field)
			if ok && responseKey(field) == alias {
				conflict = true
				break
			}
		}
		if !conflict {
			return alias
		}
	}
}

func writeChildType(out *strings.Builder, typ *ast.Type, reverse map[string]string) {
	if typ.Elem != nil {
		out.WriteString("[")
		writeChildType(out, typ.Elem, reverse)
		out.WriteString("]")
	} else if child := reverse[typ.NamedType]; child != "" {
		out.WriteString(child)
	} else {
		out.WriteString(typ.NamedType)
	}
	if typ.NonNull {
		out.WriteString("!")
	}
}

func responseKey(field *ast.Field) string {
	if field.Alias != "" {
		return field.Alias
	}
	return field.Name
}

func executeChild(ctx context.Context, endpoint Endpoint, query string, variables map[string]any) (body []byte, err error) {
	return safelyCall(func() ([]byte, error) {
		return endpoint.ExecuteOperation(withDelegatedExecution(ctx), query, variables, "")
	})
}

func subscribeChild(ctx context.Context, endpoint SubscriptionEndpoint, query string, variables map[string]any) (events <-chan []byte, err error) {
	return safelyCall(func() (<-chan []byte, error) {
		return endpoint.Subscribe(withDelegatedExecution(ctx), query, variables, "")
	})
}

func endpointError(key string, field *ast.Field, _ error) gqlerror.List {
	gqlErr := gqlerror.Errorf("endpoint unavailable")
	gqlErr.Extensions = map[string]any{"code": "REMOTE_UNAVAILABLE"}
	if key != "" {
		gqlErr.Path = ast.Path{ast.PathName(key)}
	}
	setPublicLocation(gqlErr, field)
	return gqlerror.List{gqlErr}
}

func endpointProtocolError(key string, field *ast.Field) gqlerror.List {
	err := gqlerror.Errorf("endpoint returned an invalid GraphQL response")
	err.Path = ast.Path{ast.PathName(key)}
	err.Extensions = map[string]any{"code": "BAD_GATEWAY_RESPONSE"}
	setPublicLocation(err, field)
	return gqlerror.List{err}
}

func indeterminateCommitError(key string, field *ast.Field) gqlerror.List {
	err := gqlerror.Errorf("mutation commit status is indeterminate")
	err.Path = ast.Path{ast.PathName(key)}
	err.Extensions = map[string]any{"code": "INDETERMINATE_COMMIT"}
	setPublicLocation(err, field)
	return gqlerror.List{err}
}

func markIndeterminateCommit(errors gqlerror.List) {
	for _, err := range errors {
		if err.Extensions == nil {
			err.Extensions = make(map[string]any)
		}
		err.Extensions["code"] = "INDETERMINATE_COMMIT"
	}
}

func prefixErrors(errors gqlerror.List, prefix string, field *ast.Field) {
	prefixEndpointErrors(errors, prefix, field, true)
}

func prefixLocalErrors(errors gqlerror.List, prefix string, field *ast.Field) {
	prefixEndpointErrors(errors, prefix, field, false)
}

func prefixEndpointErrors(errors gqlerror.List, prefix string, field *ast.Field, sanitize bool) {
	for _, err := range errors {
		set := ast.SelectionSet{field}
		if prefix != "" {
			set = field.SelectionSet
		}
		position, valid := childErrorPosition(set, err.Path)
		if !valid {
			err.Message = "endpoint returned an invalid error path"
			err.Path = nil
			err.Extensions = map[string]any{"code": "BAD_GATEWAY_RESPONSE"}
			position = field
		} else if sanitize {
			code, _ := err.Extensions["code"].(string)
			if !publicRemoteErrorCode(code) {
				code = "REMOTE_ERROR"
			}
			err.Message = "endpoint execution failed"
			err.Extensions = map[string]any{"code": code}
		}
		if prefix != "" {
			err.Path = append(ast.Path{ast.PathName(prefix)}, err.Path...)
		}
		setPublicLocation(err, position)
	}
}

func publicRemoteErrorCode(code string) bool {
	switch code {
	case "BAD_USER_INPUT", "BAD_GATEWAY_RESPONSE", "FORBIDDEN", "UNAUTHENTICATED", "NOT_FOUND", "CONFLICT",
		"LIMIT_EXCEEDED", "TIMEOUT", "CANCELLED", "POLICY_DENIED", "POLICY_REVISION_MISMATCH",
		"SNAPSHOT_CHANGED", "SNAPSHOT_CONFLICT", "GRAPHQL_ERROR", "INDETERMINATE_COMMIT":
		return true
	default:
		return false
	}
}

func childErrorPosition(set ast.SelectionSet, path ast.Path) (*ast.Field, bool) {
	var position *ast.Field
	for _, element := range path {
		name, ok := element.(ast.PathName)
		if !ok { // List indices do not change the selected field.
			if _, index := element.(ast.PathIndex); index {
				continue
			}
			return nil, false
		}
		position = selectedField(set, string(name))
		if position == nil {
			return nil, false
		}
		set = position.SelectionSet
	}
	return position, position != nil
}

func selectedField(set ast.SelectionSet, key string) *ast.Field {
	for _, selection := range set {
		if field, ok := selection.(*ast.Field); ok && responseKey(field) == key {
			return field
		}
	}
	return nil
}

func setPublicLocation(err *gqlerror.Error, field *ast.Field) {
	if field != nil && field.Position != nil {
		err.Locations = []gqlerror.Location{{Line: field.Position.Line, Column: field.Position.Column}}
	}
}

// completeEndpointValue projects untrusted child JSON through the public
// selection and reapplies GraphQL Non-Null bubbling before it crosses the
// namespace boundary.
func completeEndpointValue(schema *ast.Schema, value any, typ *ast.Type, set ast.SelectionSet, path ast.Path, known gqlerror.List, contract *namespaceContract) (any, gqlerror.List, bool) {
	return completeValue(schema, value, typ, set, path, known, completionOptions{mode: completionRemote, typename: contract.publicTypename})
}

func (my *namespaceContract) publicTypename(name string) string {
	if name == "" || name != "Query" && my.publicToChild[name] != "" {
		return name
	}
	return my.prefix + name
}

func typeConditionApplies(schema *ast.Schema, condition, runtimeType string) bool {
	if condition == "" || condition == runtimeType {
		return true
	}
	for _, possible := range schema.PossibleTypes[condition] {
		if possible.Name == runtimeType {
			return true
		}
	}
	return false
}
