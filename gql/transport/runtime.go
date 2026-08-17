package transport

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	core "github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/internal/lru"
	"github.com/ichaly/ideabase/gql/internal/sharedflight"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"golang.org/x/sync/singleflight"
)

const EndpointProtocolVersion = 1

// RemoteProblemError is the bounded, machine-readable control-plane failure
// contract. It is shared by HTTP and SSE so retry policy never depends on a
// status-code-only guess.
type RemoteProblemError struct {
	Operation       string
	Version         int
	Code            string
	Category        string
	Retryable       bool
	Details         map[string]any
	CurrentRevision uint64
	Message         string
}

func (e *RemoteProblemError) Error() string {
	if e.Message != "" {
		return e.Operation + ": " + e.Message
	}
	return e.Operation + ": " + e.Code
}

func (e *RemoteProblemError) ErrorCode() string { return e.Code }

type remoteProblemWire struct {
	Version int `json:"version"`
	Error   struct {
		Code            string         `json:"code"`
		Category        string         `json:"category"`
		Message         string         `json:"message"`
		Retryable       bool           `json:"retryable"`
		Details         map[string]any `json:"details"`
		CurrentRevision uint64         `json:"currentRevision"`
	} `json:"error"`
}

func remoteResponseError(resp *http.Response, operation string, limit int64) error {
	body, readErr := readLimited(resp.Body, limit)
	if readErr != nil {
		return fmt.Errorf("remote %s status %d: %w", operation, resp.StatusCode, readErr)
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "application/problem+json" {
		var wire remoteProblemWire
		if err := decodeJSON(body, &wire); err == nil && wire.Error.Code != "" {
			return &RemoteProblemError{Operation: operation, Version: wire.Version, Code: wire.Error.Code, Category: wire.Error.Category, Retryable: wire.Error.Retryable, Details: wire.Error.Details, CurrentRevision: wire.Error.CurrentRevision, Message: wire.Error.Message}
		}
	}
	return fmt.Errorf("remote %s status %d content type %q", operation, resp.StatusCode, mediaType)
}

func remoteProblemRetryable(err error, fallback bool) bool {
	var problem *RemoteProblemError
	if errors.As(err, &problem) {
		return problem.Retryable
	}
	return fallback
}

// Manifest is the immutable wire contract used to compose a remote endpoint.
type Manifest struct {
	Version      int               `json:"version"`
	Revision     uint64            `json:"revision"`
	EndpointID   string            `json:"endpointId"`
	Schema       string            `json:"schema"`
	Fingerprint  string            `json:"fingerprint"`
	Capabilities []core.Capability `json:"capabilities"`
	Subscription bool              `json:"subscription"`
	Health       bool              `json:"health"`
}

type manifestEndpoint interface {
	Snapshot() (*core.Snapshot, error)
}

type manifestStateEndpoint interface {
	ManifestState() (*core.Snapshot, uint64, error)
}

type effectiveSchemaEndpoint interface {
	EffectiveSchema(context.Context) (core.SchemaArtifact, error)
}

// ManifestAuthenticator is the explicit trust boundary for schema metadata.
type ManifestAuthenticator func(*http.Request) bool

func schemaDigest(schema string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(schema)))
}

// NewEffectiveSchemaHandler exposes identity-aware schema materialization over
// one authenticated control-plane path.
func NewEffectiveSchemaHandler(endpoint effectiveSchemaEndpoint, verifier DelegationVerifier) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeProblem(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", false)
			return
		}
		if endpoint == nil || verifier == nil {
			writeProblem(w, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "effective schema authentication required", false)
			return
		}
		token, ok := delegationToken(r)
		if !ok {
			writeProblem(w, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "delegation required", false)
			return
		}
		revision := r.Header.Get("GQL-Snapshot")
		ctx, err := verifier.Verify(r.Context(), token, operationDigest(request{}, "effective-schema", revision))
		if err != nil {
			writeProblem(w, http.StatusUnauthorized, "INVALID_DELEGATION", "invalid delegation", false)
			return
		}
		effective := endpoint
		if revision != "" {
			pinner, ok := endpoint.(snapshotPinner)
			if !ok {
				writeProblem(w, http.StatusConflict, "SNAPSHOT_UNAVAILABLE", "snapshot pinning unavailable", true)
				return
			}
			pinned, pinErr := pinner.ForSnapshot(revision)
			var effectiveOK bool
			effective, effectiveOK = pinned.(effectiveSchemaEndpoint)
			if pinErr != nil || !effectiveOK {
				writeProblem(w, http.StatusConflict, "SNAPSHOT_CHANGED", "snapshot revision changed", true)
				return
			}
		}
		artifact, err := effective.EffectiveSchema(ctx)
		if err != nil {
			writeProblem(w, http.StatusServiceUnavailable, "EFFECTIVE_SCHEMA_UNAVAILABLE", "effective schema unavailable", true)
			return
		}
		if artifact.Schema == "" || artifact.Fingerprint != schemaDigest(artifact.Schema) || core.ValidateSchemaArtifact(artifact) != nil {
			writeProblem(w, http.StatusServiceUnavailable, "INVALID_EFFECTIVE_SCHEMA", "invalid effective schema", false)
			return
		}
		if revision != "" && artifact.StructuralFingerprint != revision {
			writeProblem(w, http.StatusConflict, "SNAPSHOT_CHANGED", "snapshot revision changed", true)
			return
		}
		if revision == "" {
			revision = artifact.StructuralFingerprint
		}
		w.Header().Set("GQL-Snapshot", revision)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(artifact)
	})
}

// NewManifestHandler exposes one frozen runtime manifest for remote composition.
func NewManifestHandler(id string, endpoint manifestEndpoint, authenticate ManifestAuthenticator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authenticate == nil || !authenticate(r) {
			writeProblem(w, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "manifest authentication required", false)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeProblem(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", false)
			return
		}
		if id == "" || endpoint == nil {
			writeProblem(w, http.StatusServiceUnavailable, "MANIFEST_UNAVAILABLE", "manifest unavailable", true)
			return
		}
		snapshot, revision, err := publishedManifest(endpoint)
		if err != nil {
			writeProblem(w, http.StatusServiceUnavailable, "MANIFEST_UNAVAILABLE", "manifest unavailable", true)
			return
		}
		manifest := Manifest{
			Version: EndpointProtocolVersion, EndpointID: id, Schema: snapshot.Schema(), Fingerprint: snapshot.Fingerprint(),
			Capabilities: snapshot.Capabilities(),
		}
		manifest.Revision = revision
		if source, ok := endpoint.(interface{ SubscriptionAvailable() bool }); ok {
			manifest.Subscription = source.SubscriptionAvailable()
		}
		if source, ok := endpoint.(interface{ HealthAvailable() bool }); ok {
			manifest.Health = source.HealthAvailable()
		} else if _, ok := endpoint.(interface {
			Health(context.Context) (core.HealthReport, error)
		}); ok {
			manifest.Health = true
		}
		w.Header().Set("Content-Type", "application/json")
		etag := fmt.Sprintf(`"%d:%s"`, revision, snapshot.Fingerprint())
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_ = json.NewEncoder(w).Encode(manifest)
	})
}

func publishedManifest(endpoint manifestEndpoint) (*core.Snapshot, uint64, error) {
	if source, ok := endpoint.(manifestStateEndpoint); ok {
		return source.ManifestState()
	}
	snapshot, err := endpoint.Snapshot()
	return snapshot, 0, err
}

// RemoteOptions fixes every network boundary at startup; redirects are always denied.
type RemoteOptions struct {
	EndpointID, ManifestURL, EffectiveSchemaURL, ExecuteURL, SubscribeURL, HealthURL string
	Client                                                                           *http.Client
	Delegation                                                                       interface {
		Sign(context.Context, string) (string, error)
	}
	Timeout           time.Duration
	EffectiveCacheTTL time.Duration
	MaxResponseBytes  int64
	MaxConcurrent     int
	MaxSubscriptions  int
	AllowHTTP         bool
}

// RemoteEndpoint implements gql.Endpoint over GraphQL-over-HTTP.
type RemoteEndpoint struct {
	id        string
	manifest  string
	effective string
	execute   string
	client    *http.Client
	signer    interface {
		Sign(context.Context, string) (string, error)
	}
	maxResponse       int64
	timeout           time.Duration
	idleTimeout       time.Duration
	subscribe         string
	health            string
	sseRecoveryWindow time.Duration
	retryDelay        func(int) time.Duration
	state             atomic.Pointer[remoteState]
	refresh           sync.Mutex
	historyMu         sync.RWMutex
	history           map[string]*remoteState
	order             []string
	unarySlots        chan struct{}
	subscriptionSlots chan struct{}
	effectiveCache    *remoteEffectiveCache
	effectiveCacheTTL time.Duration
	clock             func() time.Time
}

type remoteEffectiveCache struct {
	entries *lru.Cache[string, remoteEffectiveEntry]
	flight  singleflight.Group
}

type remoteEffectiveEntry struct {
	artifact core.SchemaArtifact
	expires  time.Time
}

const (
	remoteSSERecoveryWindow  = 30 * time.Second
	remoteSSEBackoff         = 100 * time.Millisecond
	remoteSSEMaxBackoff      = 5 * time.Second
	remoteMaxConcurrent      = 128
	remoteMaxSubscriptions   = 1024
	defaultEffectiveCacheTTL = 5 * time.Second
)

var errRemoteSSEStream = errors.New("remote subscription stream failed")

func (my *RemoteEndpoint) ControlState() core.ControlState {
	state := my.state.Load()
	if state == nil || state.snapshot == nil {
		return core.ControlState{}
	}
	return core.ControlState{Revision: state.revision, Fingerprint: state.snapshot.Fingerprint()}
}

// remoteState is published as one pointer so snapshot, subscription and
// health capability bits always describe the same manifest revision.
type remoteState struct {
	snapshot              *core.Snapshot
	revision              uint64
	subscriptionAvailable bool
	healthAvailable       bool
}

type remoteRevision struct {
	endpoint              *RemoteEndpoint
	snapshot              *core.Snapshot
	subscriptionAvailable bool
	healthAvailable       bool
}

func (my *remoteRevision) EndpointID() string                { return my.endpoint.id }
func (my *remoteRevision) Snapshot() (*core.Snapshot, error) { return my.snapshot, nil }
func (my *remoteRevision) Execute(ctx context.Context, query string, variables map[string]any) ([]byte, error) {
	return my.endpoint.executeOperation(ctx, query, variables, "", my.snapshot.Fingerprint())
}
func (my *remoteRevision) ExecuteOperation(ctx context.Context, query string, variables map[string]any, operationName string) ([]byte, error) {
	return my.endpoint.executeOperation(ctx, query, variables, operationName, my.snapshot.Fingerprint())
}
func (my *remoteRevision) Subscribe(ctx context.Context, query string, variables map[string]any, operationName string) (<-chan []byte, error) {
	if !my.subscriptionAvailable {
		return nil, fmt.Errorf("remote subscription unavailable for snapshot")
	}
	return my.endpoint.subscribeOperation(ctx, query, variables, operationName, my.snapshot.Fingerprint())
}
func (my *remoteRevision) SubscriptionAvailable() bool { return my.subscriptionAvailable }
func (my *remoteRevision) HealthAvailable() bool       { return my.healthAvailable }
func (my *remoteRevision) Health(ctx context.Context) (core.HealthReport, error) {
	return my.endpoint.healthFor(ctx, my.snapshot.Fingerprint())
}
func (my *remoteRevision) EffectiveSchema(ctx context.Context) (core.SchemaArtifact, error) {
	if my.endpoint.effective == "" {
		return staticSchemaArtifact(my.snapshot), nil
	}
	return my.endpoint.effectiveSchema(ctx, my.snapshot.Fingerprint())
}
func (my *remoteRevision) EffectiveSchemaAvailable() bool {
	return my.endpoint.EffectiveSchemaAvailable()
}
func (my *remoteRevision) EffectiveSchemaCacheIdentity(ctx context.Context) (string, bool) {
	return my.endpoint.cachedEffectiveSchemaIdentity(ctx, my.snapshot.Fingerprint())
}

func NewRemoteEndpoint(ctx context.Context, options RemoteOptions) (*RemoteEndpoint, error) {
	if options.EndpointID == "" || options.ManifestURL == "" || options.ExecuteURL == "" {
		return nil, fmt.Errorf("remote endpoint id, manifest URL and execute URL are required")
	}
	urls := []string{options.ManifestURL, options.ExecuteURL}
	if options.EffectiveSchemaURL != "" {
		urls = append(urls, options.EffectiveSchemaURL)
	}
	if options.SubscribeURL != "" {
		urls = append(urls, options.SubscribeURL)
	}
	if options.HealthURL != "" {
		urls = append(urls, options.HealthURL)
	}
	for _, raw := range urls {
		if !remoteURLAllowed(raw, options.AllowHTTP) {
			return nil, fmt.Errorf("remote endpoint URL is not allowed: %s", raw)
		}
	}
	if options.Timeout <= 0 {
		options.Timeout = 10 * time.Second
	}
	if options.MaxResponseBytes <= 0 {
		options.MaxResponseBytes = 8 << 20
	}
	if options.MaxConcurrent <= 0 {
		options.MaxConcurrent = remoteMaxConcurrent
	}
	if options.MaxSubscriptions <= 0 {
		options.MaxSubscriptions = remoteMaxSubscriptions
	}
	if options.EffectiveCacheTTL <= 0 {
		options.EffectiveCacheTTL = defaultEffectiveCacheTTL
	}
	client := options.Client
	if client == nil {
		client = &http.Client{}
	} else {
		copy := *client
		client = &copy
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	remote := &RemoteEndpoint{
		id: options.EndpointID, manifest: options.ManifestURL, effective: options.EffectiveSchemaURL, execute: options.ExecuteURL,
		client: client, signer: options.Delegation, maxResponse: options.MaxResponseBytes, timeout: options.Timeout, subscribe: options.SubscribeURL,
		health:      options.HealthURL,
		idleTimeout: 30 * time.Second,
		history:     make(map[string]*remoteState, 8),
		unarySlots:  make(chan struct{}, options.MaxConcurrent), subscriptionSlots: make(chan struct{}, options.MaxSubscriptions),
		effectiveCache: newRemoteEffectiveCache(64), effectiveCacheTTL: options.EffectiveCacheTTL, clock: time.Now,
	}
	if err := remote.Refresh(ctx); err != nil {
		return nil, err
	}
	return remote, nil
}

func acquireRemoteSlot(ctx context.Context, slots chan struct{}, operation string) (func(), error) {
	if slots == nil {
		return func() {}, nil
	}
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return nil, &RemoteProblemError{Operation: operation, Code: "REMOTE_OVERLOADED", Message: "remote endpoint capacity exceeded", Retryable: true}
	}
}

// EffectiveSchema fetches an identity-bound schema artifact. It never falls
// back to the static manifest because doing so would leak or over-advertise
// fields hidden by the child policy.
func (my *RemoteEndpoint) EffectiveSchema(ctx context.Context) (core.SchemaArtifact, error) {
	state := my.state.Load()
	if state == nil || state.snapshot == nil {
		return core.SchemaArtifact{}, fmt.Errorf("remote manifest unavailable")
	}
	return my.effectiveSchema(ctx, state.snapshot.Fingerprint())
}

func (my *RemoteEndpoint) EffectiveSchemaAvailable() bool { return my != nil && my.effective != "" }

func (my *RemoteEndpoint) EffectiveSchemaCacheIdentity(ctx context.Context) (string, bool) {
	state := my.state.Load()
	if state == nil || state.snapshot == nil || my.effective == "" {
		return "", false
	}
	return my.cachedEffectiveSchemaIdentity(ctx, state.snapshot.Fingerprint())
}

func newRemoteEffectiveCache(limit int) *remoteEffectiveCache {
	if limit <= 0 {
		limit = 64
	}
	return &remoteEffectiveCache{entries: lru.New[string, remoteEffectiveEntry](limit)}
}

func (cache *remoteEffectiveCache) get(key string, now time.Time) (core.SchemaArtifact, bool) {
	entry, ok := cache.entries.GetIf(key, func(entry remoteEffectiveEntry) bool { return entry.expires.After(now) })
	return entry.artifact, ok
}

func (cache *remoteEffectiveCache) put(key string, artifact core.SchemaArtifact, expires time.Time) {
	cache.entries.Put(key, remoteEffectiveEntry{artifact: artifact, expires: expires})
}

func (my *RemoteEndpoint) effectiveSchemaRequestKey(ctx context.Context, structural string) (string, bool) {
	identity, ok := core.IdentityFromContext(ctx)
	if !ok {
		return "", false
	}
	state := my.state.Load()
	if state == nil {
		return "", false
	}
	key, err := json.Marshal(struct {
		Structural string
		Revision   uint64
		Tenant     string
		Subject    string
		Actor      string
		Roles      []string
		Claims     map[string]any
		Policy     uint64
		Credential uint64
		Scope      map[string]any
	}{structural, state.revision, identity.Tenant(), identity.Subject(), identity.Actor(), identity.Roles(), identity.Claims(), identity.PolicyRevision(), identity.CredentialEpoch(), core.Scope(ctx)})
	if err != nil {
		return "", false
	}
	return schemaDigest(string(key)), true
}

func (my *RemoteEndpoint) cachedEffectiveSchemaIdentity(ctx context.Context, structural string) (string, bool) {
	key, ok := my.effectiveSchemaRequestKey(ctx, structural)
	if !ok || my.effectiveCache == nil {
		return "", false
	}
	artifact, ok := my.effectiveCache.get(key, my.effectiveNow())
	if !ok || strings.TrimSpace(artifact.CacheIdentity) == "" {
		return "", false
	}
	return artifact.CacheIdentity, true
}

func (my *RemoteEndpoint) effectiveSchema(ctx context.Context, structural string) (core.SchemaArtifact, error) {
	if my.effectiveCache == nil {
		my.effectiveCache = newRemoteEffectiveCache(64)
	}
	key, cacheable := my.effectiveSchemaRequestKey(ctx, structural)
	if cacheable {
		if artifact, ok := my.effectiveCache.get(key, my.effectiveNow()); ok {
			return artifact, nil
		}
		return sharedflight.Do(ctx, &my.effectiveCache.flight, key, my.timeout, func(shared context.Context) (core.SchemaArtifact, error) {
			if artifact, ok := my.effectiveCache.get(key, my.effectiveNow()); ok {
				return artifact, nil
			}
			artifact, err := my.effectiveSchemaFetch(shared, structural)
			if err != nil {
				return core.SchemaArtifact{}, err
			}
			if strings.TrimSpace(artifact.CacheIdentity) != "" {
				my.effectiveCache.put(key, artifact, my.effectiveNow().Add(my.effectiveTTL()))
			}
			return artifact, nil
		})
	}
	return my.effectiveSchemaFetch(ctx, structural)
}

func (my *RemoteEndpoint) effectiveNow() time.Time {
	if my.clock != nil {
		return my.clock()
	}
	return time.Now()
}

func (my *RemoteEndpoint) effectiveTTL() time.Duration {
	if my.effectiveCacheTTL > 0 {
		return my.effectiveCacheTTL
	}
	return defaultEffectiveCacheTTL
}

func (my *RemoteEndpoint) effectiveSchemaFetch(ctx context.Context, structural string) (core.SchemaArtifact, error) {
	if my == nil {
		return core.SchemaArtifact{}, fmt.Errorf("remote effective schema unavailable")
	}
	if my.effective == "" {
		state := my.state.Load()
		if state == nil || state.snapshot == nil || state.snapshot.Fingerprint() != structural {
			return core.SchemaArtifact{}, fmt.Errorf("remote manifest unavailable")
		}
		return staticSchemaArtifact(state.snapshot), nil
	}
	ctx, cancel := context.WithTimeout(ctx, my.timeout)
	defer cancel()
	release, err := acquireRemoteSlot(ctx, my.unarySlots, "effective schema")
	if err != nil {
		return core.SchemaArtifact{}, err
	}
	defer release()
	reqPayload := request{}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, my.effective, nil)
	if err != nil {
		return core.SchemaArtifact{}, err
	}
	req.Header.Set("Accept", "application/json")
	if my.signer == nil {
		return core.SchemaArtifact{}, fmt.Errorf("remote effective schema requires delegation")
	}
	token, err := my.signer.Sign(ctx, operationDigest(reqPayload, "effective-schema", structural))
	if err != nil {
		return core.SchemaArtifact{}, err
	}
	req.Header.Set("Authorization", delegationScheme+token)
	req.Header.Set("GQL-Snapshot", structural)
	resp, err := my.client.Do(req)
	if err != nil {
		return core.SchemaArtifact{}, fmt.Errorf("remote effective schema unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return core.SchemaArtifact{}, remoteResponseError(resp, "effective schema", my.maxResponse)
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		return core.SchemaArtifact{}, fmt.Errorf("remote effective schema content type %q", mediaType)
	}
	var artifact core.SchemaArtifact
	if err := decodeResponse(resp.Body, my.maxResponse, &artifact); err != nil {
		return core.SchemaArtifact{}, err
	}
	if artifact.Schema == "" || artifact.Fingerprint == "" || artifact.StructuralFingerprint != structural {
		return core.SchemaArtifact{}, fmt.Errorf("remote effective schema is invalid")
	}
	if resp.Header.Get("GQL-Snapshot") != structural {
		return core.SchemaArtifact{}, fmt.Errorf("remote effective schema snapshot mismatch")
	}
	if artifact.Fingerprint != schemaDigest(artifact.Schema) {
		return core.SchemaArtifact{}, fmt.Errorf("remote effective schema fingerprint mismatch")
	}
	if err := core.ValidateSchemaArtifact(artifact); err != nil {
		return core.SchemaArtifact{}, err
	}
	if err := core.ValidateArtifactIdentity(ctx, artifact); err != nil {
		return core.SchemaArtifact{}, err
	}
	return artifact, nil
}

func staticSchemaArtifact(snapshot *core.Snapshot) core.SchemaArtifact {
	return core.FinalizeSchemaArtifact(core.SchemaArtifact{Schema: snapshot.Schema(), Fingerprint: schemaDigest(snapshot.Schema()), StructuralFingerprint: snapshot.Fingerprint(), Capabilities: snapshot.Capabilities()})
}

func remoteURLAllowed(raw string, allowHTTP bool) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	host := parsed.Hostname()
	return allowHTTP && parsed.Scheme == "http" && (strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback())
}

func (my *RemoteEndpoint) EndpointID() string { return my.id }
func (my *RemoteEndpoint) ManifestRevision() uint64 {
	state := my.state.Load()
	if state == nil {
		return 0
	}
	return state.revision
}

func (my *RemoteEndpoint) ManifestState() (*core.Snapshot, uint64, error) {
	state := my.state.Load()
	if state == nil || state.snapshot == nil {
		return nil, 0, fmt.Errorf("remote manifest unavailable")
	}
	return state.snapshot, state.revision, nil
}

func (my *RemoteEndpoint) SubscriptionAvailable() bool {
	state := my.state.Load()
	return state != nil && state.subscriptionAvailable
}
func (my *RemoteEndpoint) HealthAvailable() bool {
	state := my.state.Load()
	return state != nil && state.healthAvailable
}

func (my *RemoteEndpoint) Snapshot() (*core.Snapshot, error) {
	state := my.state.Load()
	if state == nil || state.snapshot == nil {
		return nil, fmt.Errorf("remote snapshot unavailable")
	}
	return state.snapshot, nil
}

func (my *RemoteEndpoint) ForSnapshot(fingerprint string) (core.ComposableEndpoint, error) {
	state := my.state.Load()
	if state == nil || state.snapshot == nil || state.snapshot.Fingerprint() != fingerprint {
		my.historyMu.Lock()
		state = my.history[fingerprint]
		if state == nil {
			my.historyMu.Unlock()
			return nil, fmt.Errorf("remote snapshot revision unavailable")
		}
		for i, revision := range my.order {
			if revision == fingerprint {
				copy(my.order[i:], my.order[i+1:])
				my.order[len(my.order)-1] = fingerprint
				break
			}
		}
		my.historyMu.Unlock()
	}
	return &remoteRevision{endpoint: my, snapshot: state.snapshot, subscriptionAvailable: state.subscriptionAvailable, healthAvailable: state.healthAvailable}, nil
}

// Refresh atomically installs a fully validated manifest or retains the LKG snapshot.
func (my *RemoteEndpoint) Refresh(ctx context.Context) error {
	my.refresh.Lock()
	defer my.refresh.Unlock()
	ctx, cancel := context.WithTimeout(ctx, my.timeout)
	defer cancel()
	release, err := acquireRemoteSlot(ctx, my.unarySlots, "manifest")
	if err != nil {
		return err
	}
	defer release()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, my.manifest, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if current := my.state.Load(); current != nil && current.snapshot != nil {
		req.Header.Set("If-None-Match", fmt.Sprintf(`"%d:%s"`, current.revision, current.snapshot.Fingerprint()))
	}
	if my.signer != nil {
		if _, ok := core.IdentityFromContext(ctx); ok {
			token, signErr := my.signer.Sign(ctx, operationDigest(request{}, "manifest", ""))
			if signErr != nil {
				return signErr
			}
			req.Header.Set("Authorization", delegationScheme+token)
		}
	}
	response, err := my.client.Do(req)
	if err != nil {
		return fmt.Errorf("remote manifest unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		return nil
	}
	if response.StatusCode != http.StatusOK {
		return remoteResponseError(response, "manifest", my.maxResponse)
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		return fmt.Errorf("remote manifest content type %q", mediaType)
	}
	var manifest Manifest
	if err := decodeResponse(response.Body, my.maxResponse, &manifest); err != nil {
		return fmt.Errorf("remote manifest: %w", err)
	}
	if manifest.Version != EndpointProtocolVersion || manifest.EndpointID != my.id {
		return fmt.Errorf("remote manifest identity or protocol mismatch")
	}
	current := my.state.Load()
	if current != nil {
		if manifest.Revision < current.revision {
			return fmt.Errorf("%w: %d", core.ErrControlStale, manifest.Revision)
		}
		if manifest.Revision == current.revision && current.snapshot.Fingerprint() != manifest.Fingerprint {
			return fmt.Errorf("%w: %d", core.ErrControlConflict, manifest.Revision)
		}
	}
	parsed, err := gqlparser.LoadSchema(&ast.Source{Name: "remote.graphql", Input: manifest.Schema})
	if err != nil || (parsed.Subscription != nil) != manifest.Subscription || (manifest.Subscription && my.subscribe == "") {
		return fmt.Errorf("remote subscription manifest is inconsistent")
	}
	if manifest.Health != (my.health != "") {
		return fmt.Errorf("remote health manifest is inconsistent")
	}
	snapshot, err := core.NewSnapshotArtifact(manifest.Schema, manifest.Fingerprint, manifest.Capabilities)
	if err != nil {
		return err
	}
	next := &remoteState{snapshot: snapshot, revision: manifest.Revision, subscriptionAvailable: manifest.Subscription, healthAvailable: manifest.Health}
	if current != nil && current.snapshot.Fingerprint() != snapshot.Fingerprint() {
		my.historyMu.Lock()
		for index, revision := range my.order {
			if revision == snapshot.Fingerprint() {
				delete(my.history, revision)
				my.order = append(my.order[:index], my.order[index+1:]...)
				break
			}
		}
		fingerprint := current.snapshot.Fingerprint()
		my.history[fingerprint] = current
		my.order = append(my.order, fingerprint)
		if len(my.order) > 8 {
			delete(my.history, my.order[0])
			my.order = my.order[1:]
		}
		my.historyMu.Unlock()
	}
	my.state.Store(next)
	return nil
}

func (my *RemoteEndpoint) Execute(ctx context.Context, query string, variables map[string]any) ([]byte, error) {
	return my.ExecuteOperation(ctx, query, variables, "")
}

func (my *RemoteEndpoint) ExecuteOperation(ctx context.Context, query string, variables map[string]any, operationName string) ([]byte, error) {
	return my.executeOperation(ctx, query, variables, operationName, "")
}

func (my *RemoteEndpoint) executeOperation(ctx context.Context, query string, variables map[string]any, operationName, fingerprint string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, my.timeout)
	defer cancel()
	release, err := acquireRemoteSlot(ctx, my.unarySlots, "execute")
	if err != nil {
		return nil, err
	}
	defer release()
	payload := request{Query: query, OperationName: operationName, Variables: variables}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, my.execute, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", responseType)
	if core.MutationCommitMetadataRequested(ctx) {
		request.Header.Set("GQL-Mutation-Commit", "1")
	}
	if fingerprint != "" {
		request.Header.Set("GQL-Snapshot", fingerprint)
	}
	pin, _ := core.ExecutionPinFromContext(ctx)
	if pin != "" {
		request.Header.Set(executionPinHeader, pin)
	}
	if my.signer != nil {
		token, signErr := my.signer.Sign(ctx, operationDigest(payload, fingerprint, pin))
		if signErr != nil {
			return nil, signErr
		}
		request.Header.Set("Authorization", delegationScheme+token)
	}
	response, err := my.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("remote execute unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, remoteResponseError(response, "execute", my.maxResponse)
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType != responseType {
		return nil, fmt.Errorf("remote execute status %d content type %q", response.StatusCode, mediaType)
	}
	if fingerprint != "" && response.Header.Get("GQL-Snapshot") != fingerprint {
		return nil, fmt.Errorf("remote execute snapshot mismatch")
	}
	limit := my.maxResponse
	if requested := core.ResponseLimit(ctx); requested > 0 && requested < limit {
		limit = requested
	}
	return readLimited(response.Body, limit)
}

// Subscribe consumes the GraphQL SSE single-operation protocol.
func (my *RemoteEndpoint) Subscribe(ctx context.Context, query string, variables map[string]any, operationName string) (<-chan []byte, error) {
	return my.subscribeOperation(ctx, query, variables, operationName, "")
}

func (my *RemoteEndpoint) subscribeOperation(ctx context.Context, query string, variables map[string]any, operationName, fingerprint string) (<-chan []byte, error) {
	if my.subscribe == "" {
		return nil, fmt.Errorf("remote subscription unavailable")
	}
	release, err := acquireRemoteSlot(ctx, my.subscriptionSlots, "subscription")
	if err != nil {
		return nil, err
	}
	payload := request{Query: query, OperationName: operationName, Variables: variables}
	body, err := json.Marshal(payload)
	if err != nil {
		release()
		return nil, err
	}
	streamCtx, cancelStream := context.WithCancel(ctx)
	response, err, _ := my.openSubscription(streamCtx, payload, body, fingerprint)
	if err != nil {
		cancelStream()
		release()
		return nil, err
	}
	events := make(chan []byte, 1)
	go func() {
		defer release()
		defer cancelStream()
		defer close(events)
		attempt := 0
		window := my.sseRecoveryWindow
		if window <= 0 {
			window = remoteSSERecoveryWindow
		}
		deadline := time.Now().Add(window)
		for {
			complete, readErr, progressed := my.readSubscriptionProgress(streamCtx, response.Body, events)
			response.Body.Close()
			if complete || streamCtx.Err() != nil {
				return
			}
			if readErr != nil && !retryableReadError(readErr) {
				sendStreamError(streamCtx, events, readErr.Error())
				return
			}
			if progressed {
				attempt = 0
				deadline = time.Now().Add(window)
			} else {
				// A successful handshake without a next event is not progress;
				// count it toward the retry budget to avoid an EOF hot loop.
				attempt++
			}
			for {
				if time.Now().After(deadline) {
					sendStreamError(streamCtx, events, "remote subscription retry limit exceeded")
					return
				}
				delay := my.retryDelay
				if delay == nil {
					delay = remoteRetryDelay
				}
				if err := waitSubscriptionRetryWith(streamCtx, attempt, delay); err != nil {
					return
				}
				var retryable bool
				response, err, retryable = my.openSubscription(streamCtx, payload, body, fingerprint)
				if err == nil {
					break
				}
				if !retryable {
					sendStreamError(streamCtx, events, err.Error())
					return
				}
				attempt++
			}
		}
	}()
	return events, nil
}

func (my *RemoteEndpoint) openSubscription(ctx context.Context, payload request, body []byte, fingerprint string) (*http.Response, error, bool) {
	attemptCtx, cancelAttempt := context.WithCancel(ctx)
	request, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, my.subscribe, bytes.NewReader(body))
	if err != nil {
		cancelAttempt()
		return nil, err, false
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	if fingerprint != "" {
		request.Header.Set("GQL-Snapshot", fingerprint)
	}
	pin, _ := core.ExecutionPinFromContext(ctx)
	if pin != "" {
		request.Header.Set(executionPinHeader, pin)
	}
	if my.signer != nil {
		token, signErr := my.signer.Sign(ctx, operationDigest(payload, fingerprint, pin))
		if signErr != nil {
			cancelAttempt()
			return nil, signErr, false
		}
		request.Header.Set("Authorization", delegationScheme+token)
	}
	result := make(chan struct {
		response *http.Response
		err      error
	}, 1)
	go func() {
		response, err := my.client.Do(request)
		if attemptCtx.Err() != nil && response != nil {
			response.Body.Close()
			response = nil
		}
		result <- struct {
			response *http.Response
			err      error
		}{response, err}
	}()
	timer := time.NewTimer(my.timeout)
	defer timer.Stop()
	var value struct {
		response *http.Response
		err      error
	}
	select {
	case value = <-result:
	case <-timer.C:
		cancelAttempt()
		return nil, fmt.Errorf("remote subscription handshake timeout"), true
	}
	if value.err != nil {
		cancelAttempt()
		return nil, fmt.Errorf("remote subscription unavailable: %w", value.err), true
	}
	response := value.response
	response.Body = &cancelReadCloser{ReadCloser: response.Body, cancel: cancelAttempt}
	if response.StatusCode != http.StatusOK {
		err := remoteResponseError(response, "subscription", my.maxResponse)
		response.Body.Close()
		return nil, err, remoteProblemRetryable(err, response.StatusCode >= 500)
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType != "text/event-stream" {
		response.Body.Close()
		return nil, fmt.Errorf("remote subscription content type %q", mediaType), false
	}
	if fingerprint != "" && response.Header.Get("GQL-Snapshot") != fingerprint {
		response.Body.Close()
		return nil, fmt.Errorf("remote subscription snapshot mismatch"), false
	}
	return response, nil, true
}

type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (my *cancelReadCloser) Close() error {
	err := my.ReadCloser.Close()
	my.cancel()
	return err
}

func (my *RemoteEndpoint) readSubscription(ctx context.Context, body io.ReadCloser, events chan<- []byte) (bool, error) {
	complete, err, _ := my.readSubscriptionProgress(ctx, body, events)
	return complete, err
}

func (my *RemoteEndpoint) readSubscriptionProgress(ctx context.Context, body io.ReadCloser, events chan<- []byte) (bool, error, bool) {
	idle := my.idleTimeout
	if idle <= 0 {
		idle = 30 * time.Second
	}
	watchdog := time.AfterFunc(idle, func() { _ = body.Close() })
	defer watchdog.Stop()
	limit := my.maxResponse
	if requested := core.ResponseLimit(ctx); requested > 0 && requested < limit {
		limit = requested
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), int(limit))
	event, data := "", make([]byte, 0, 1024)
	progressed := false
	for scanner.Scan() {
		watchdog.Reset(idle)
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if int64(len(data)+len(payload)+1) > limit {
				return false, fmt.Errorf("remote subscription response limit exceeded"), progressed
			}
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, payload...)
		case line == "":
			if event == "complete" {
				return true, nil, progressed
			}
			if event == "next" && len(data) > 0 {
				progressed = true
				frame := append([]byte(nil), data...)
				select {
				case events <- frame:
				case <-ctx.Done():
					return false, ctx.Err(), progressed
				}
			}
			event, data = "", data[:0]
		}
	}
	if ctx.Err() != nil {
		return false, ctx.Err(), progressed
	}
	if errors.Is(scanner.Err(), bufio.ErrTooLong) {
		return false, fmt.Errorf("remote subscription response limit exceeded"), progressed
	}
	if scanner.Err() != nil {
		return false, fmt.Errorf("%w: %v", errRemoteSSEStream, scanner.Err()), progressed
	}
	return false, io.EOF, progressed
}

func retryableReadError(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, errRemoteSSEStream)
}

func waitSubscriptionRetry(ctx context.Context, attempt int) error {
	return waitSubscriptionRetryWith(ctx, attempt, remoteRetryDelay)
}

func waitSubscriptionRetryWith(ctx context.Context, attempt int, delayFunc func(int) time.Duration) error {
	delay := delayFunc(attempt)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func remoteRetryDelay(attempt int) time.Duration {
	delay := remoteSSEBackoff * time.Duration(1<<min(attempt, 6))
	if delay > remoteSSEMaxBackoff {
		delay = remoteSSEMaxBackoff
	}
	if jitter := delay / 2; jitter > 0 {
		delay += time.Duration(rand.Int63n(int64(jitter)))
	}
	return delay
}

func sendStreamError(ctx context.Context, events chan<- []byte, message string) {
	frame, _ := json.Marshal(map[string]any{"errors": []map[string]any{{"message": message, "extensions": map[string]any{"code": "BAD_GATEWAY_RESPONSE"}}}})
	select {
	case events <- frame:
	case <-ctx.Done():
	}
}

func decodeResponse(reader io.Reader, limit int64, value any) error {
	body, err := readLimited(reader, limit)
	if err != nil {
		return err
	}
	return decodeJSON(body, value)
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return body, nil
}
