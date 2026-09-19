package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/ichaly/ideabase/gql"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func newArtifact(t *testing.T, schema string) *core.Snapshot {
	t.Helper()
	parsed, err := gqlparser.LoadSchema(&ast.Source{Name: "fixture.graphql", Input: schema})
	if err != nil {
		t.Fatal(err)
	}
	var capabilities []core.Capability
	for _, root := range []*ast.Definition{parsed.Query, parsed.Mutation, parsed.Subscription} {
		if root == nil {
			continue
		}
		for _, field := range root.Fields {
			var args []core.Argument
			for _, arg := range field.Arguments {
				args = append(args, core.Argument{Name: arg.Name, Type: arg.Type.String()})
			}
			capabilities = append(capabilities, core.Capability{Coordinate: root.Name + "." + field.Name, Parent: root.Name, Name: field.Name, Type: field.Type.String(), Arguments: args, Handler: core.HandlerResolver})
		}
	}
	fingerprint, err := core.ArtifactFingerprint(schema, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := core.NewSnapshotArtifact(schema, fingerprint, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

type runtimeEndpoint struct {
	snapshot       *core.Snapshot
	effective      core.SchemaArtifact
	identity       core.Identity
	commitMetadata bool
	body           []byte
}

func testManifestHandler(id string, endpoint manifestEndpoint) http.Handler {
	return NewManifestHandler(id, endpoint, func(*http.Request) bool { return true })
}

func (e *runtimeEndpoint) EffectiveSchema(ctx context.Context) (core.SchemaArtifact, error) {
	var artifact core.SchemaArtifact
	if e.effective.Schema != "" {
		artifact = e.effective
	} else {
		artifact = core.SchemaArtifact{Schema: e.snapshot.Schema(), Fingerprint: schemaDigest(e.snapshot.Schema()), StructuralFingerprint: e.snapshot.Fingerprint(), Capabilities: e.snapshot.Capabilities()}
	}
	if identity, ok := core.IdentityFrom(ctx); ok {
		artifact.Identity = core.IdentityDescriptorFromIdentity(identity)
	}
	return core.FinalizeSchemaArtifact(artifact), nil
}

func (e *runtimeEndpoint) ForSnapshot(fingerprint string) (core.ComposableEndpoint, error) {
	if e.snapshot == nil || e.snapshot.Fingerprint() != fingerprint {
		return nil, fmt.Errorf("snapshot unavailable")
	}
	return e, nil
}

type subscriptionRuntime struct {
	*runtimeEndpoint
	events <-chan []byte
}

func (my *subscriptionRuntime) ForSnapshot(fingerprint string) (core.ComposableEndpoint, error) {
	if my.snapshot == nil || my.snapshot.Fingerprint() != fingerprint {
		return nil, fmt.Errorf("snapshot unavailable")
	}
	return my, nil
}

type healthyRuntime struct{ *runtimeEndpoint }

type deadlineHealthRuntime struct {
	*runtimeEndpoint
	deadline bool
}

type stuckHealthRuntime struct{}

func (stuckHealthRuntime) Health(context.Context) (core.HealthReport, error) { select {} }

type versionedRuntime struct {
	*runtimeEndpoint
	revision uint64
}

func (my *versionedRuntime) ManifestState() (*core.Snapshot, uint64, error) {
	return my.snapshot, my.revision, nil
}

type rollingRuntime struct {
	current  *runtimeEndpoint
	history  map[string]*runtimeEndpoint
	revision uint64
}

func (my *rollingRuntime) Snapshot() (*core.Snapshot, error) { return my.current.Snapshot() }
func (my *rollingRuntime) ManifestState() (*core.Snapshot, uint64, error) {
	return my.current.snapshot, my.revision, nil
}
func (my *rollingRuntime) EffectiveSchema(ctx context.Context) (core.SchemaArtifact, error) {
	return my.current.EffectiveSchema(ctx)
}
func (my *rollingRuntime) ForSnapshot(fingerprint string) (core.ComposableEndpoint, error) {
	endpoint := my.history[fingerprint]
	if endpoint == nil {
		return nil, fmt.Errorf("snapshot unavailable")
	}
	return endpoint, nil
}

func (my *healthyRuntime) Health(context.Context) (core.HealthReport, error) {
	return core.HealthReport{Ready: true, SnapshotOK: true, DatabaseOK: true, SnapshotFingerprint: my.snapshot.Fingerprint()}, nil
}

func (my *deadlineHealthRuntime) Health(ctx context.Context) (core.HealthReport, error) {
	_, my.deadline = ctx.Deadline()
	return core.HealthReport{Ready: true, SnapshotOK: true, DatabaseOK: true, SnapshotFingerprint: my.snapshot.Fingerprint()}, nil
}

func TestHealthHandlerAlwaysBoundsDownstreamWork(t *testing.T) {
	endpoint := &deadlineHealthRuntime{runtimeEndpoint: &runtimeEndpoint{snapshot: newArtifact(t, "type Query { hello: String }")}}
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	recorder := httptest.NewRecorder()
	NewHealthHandler(endpoint).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !endpoint.deadline {
		t.Fatalf("status/deadline = %d/%t", recorder.Code, endpoint.deadline)
	}
}

func TestHealthHandlerReturnsWhenEndpointIgnoresContext(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	ctx, cancel := context.WithTimeout(request.Context(), 10*time.Millisecond)
	defer cancel()
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		NewHealthHandler(stuckHealthRuntime{}).ServeHTTP(recorder, request.WithContext(ctx))
		close(done)
	}()
	select {
	case <-done:
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d", recorder.Code)
		}
	case <-time.After(time.Second):
		t.Fatal("health handler ignored its deadline")
	}
}

func TestHealthHandlerBoundsConcurrentContextIgnoringChecks(t *testing.T) {
	const maxConcurrent = 8
	started := make(chan struct{}, maxConcurrent+4)
	release := make(chan struct{})
	defer close(release)
	endpoint := healthProbeFunc(func(context.Context) (core.HealthReport, error) {
		started <- struct{}{}
		<-release
		return core.HealthReport{}, nil
	})
	handler := NewHealthHandler(endpoint)
	var done sync.WaitGroup
	for i := 0; i < maxConcurrent+4; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			ctx, cancel := context.WithTimeout(req.Context(), 10*time.Millisecond)
			defer cancel()
			handler.ServeHTTP(httptest.NewRecorder(), req.WithContext(ctx))
		}()
	}
	done.Wait()
	if got := len(started); got > maxConcurrent {
		t.Fatalf("started %d ignored-context checks, want at most %d", got, maxConcurrent)
	}
}

func TestHealthHandlerRetainsCapacityForStuckChecks(t *testing.T) {
	started := make(chan struct{}, healthMaxConcurrent+4)
	release := make(chan struct{})
	defer close(release)
	handler := NewHealthHandler(healthProbeFunc(func(context.Context) (core.HealthReport, error) {
		started <- struct{}{}
		<-release
		return core.HealthReport{}, nil
	}))
	for i := 0; i < cap(started); i++ {
		request := httptest.NewRequest(http.MethodGet, "/health", nil)
		ctx, cancel := context.WithTimeout(request.Context(), time.Millisecond)
		handler.ServeHTTP(httptest.NewRecorder(), request.WithContext(ctx))
		cancel()
	}
	if got := len(started); got != healthMaxConcurrent {
		t.Fatalf("started %d ignored-context checks, want %d", got, healthMaxConcurrent)
	}
}

type healthProbeFunc func(context.Context) (core.HealthReport, error)

func (f healthProbeFunc) Health(ctx context.Context) (core.HealthReport, error) { return f(ctx) }

func (my *subscriptionRuntime) SubscriptionAvailable() bool { return true }
func (my *subscriptionRuntime) Subscribe(ctx context.Context, _ string, _ map[string]any, _ string) (<-chan []byte, error) {
	my.identity, _ = core.IdentityFrom(ctx)
	return my.events, nil
}

func (my *runtimeEndpoint) Snapshot() (*core.Snapshot, error) { return my.snapshot, nil }
func (my *runtimeEndpoint) Execute(ctx context.Context, _ string, _ map[string]any) ([]byte, error) {
	my.identity, _ = core.IdentityFrom(ctx)
	my.commitMetadata = core.MutationCommitMetadataRequested(ctx)
	if my.body != nil {
		return my.body, nil
	}
	return []byte(`{"data":{"hello":"world"}}`), nil
}

func (my *runtimeEndpoint) ExecuteOperation(ctx context.Context, query string, vars map[string]any, _ string) ([]byte, error) {
	return my.Execute(ctx, query, vars)
}

func TestRemoteEndpointAcceptsPartialGraphQLResponseStatus(t *testing.T) {
	snapshot := newArtifact(t, "type Query { hello: String }")
	endpoint := &runtimeEndpoint{snapshot: snapshot, body: []byte(`{"data":{"hello":null},"errors":[{"message":"failed","path":["hello"]}]}`)}
	mux := http.NewServeMux()
	mux.Handle("/manifest", testManifestHandler("child", endpoint))
	mux.Handle("/graphql", NewHandler(endpoint, Options{}))
	server := httptest.NewServer(mux)
	defer server.Close()
	remote, err := NewRemoteEndpoint(context.Background(), RemoteOptions{
		EndpointID: "child", ManifestURL: server.URL + "/manifest", ExecuteURL: server.URL + "/graphql", AllowHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := remote.Execute(context.Background(), `{ hello }`, nil)
	if err != nil || string(body) != string(endpoint.body) {
		t.Fatalf("partial response = %s, %v", body, err)
	}
}

func TestRemoteEndpointHonorsCallerResponseLimit(t *testing.T) {
	snapshot := newArtifact(t, "type Query { hello: String }")
	endpoint := &runtimeEndpoint{snapshot: snapshot, body: []byte(`{"data":{"hello":"` + strings.Repeat("x", 256) + `"}}`)}
	mux := http.NewServeMux()
	mux.Handle("/manifest", testManifestHandler("child", endpoint))
	mux.Handle("/graphql", NewHandler(endpoint, Options{}))
	server := httptest.NewServer(mux)
	defer server.Close()
	remote, err := NewRemoteEndpoint(context.Background(), RemoteOptions{
		EndpointID: "child", ManifestURL: server.URL + "/manifest", ExecuteURL: server.URL + "/graphql", AllowHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = remote.Execute(core.WithResponseLimit(context.Background(), 64), `{ hello }`, nil)
	if err == nil || !strings.Contains(err.Error(), "response exceeds 64 bytes") {
		t.Fatalf("response limit error = %v", err)
	}
}

func TestRemoteEndpointEffectiveSchemaUsesDelegatedIdentity(t *testing.T) {
	// The effective schema endpoint is intentionally identity-bound; a static
	// manifest must not be reused for a privileged caller's schema.
	snapshot := newArtifact(t, "type Query { public: String secret: String }")
	key := []byte("0123456789abcdef0123456789abcdef")
	serverCodec, _ := core.NewDelegationCodec(core.DelegationConfig{Issuer: "gateway", Audience: "child", Key: key, TTL: time.Minute, Replay: core.NewMemoryReplayStore(64)})
	clientCodec, _ := core.NewDelegationCodec(core.DelegationConfig{Issuer: "gateway", Audience: "child", Key: key, TTL: time.Minute, Replay: core.NewMemoryReplayStore(64)})
	effective := newArtifact(t, "type Query { public: String }")
	endpoint := &runtimeEndpoint{snapshot: snapshot, effective: core.SchemaArtifact{Schema: effective.Schema(), Fingerprint: schemaDigest(effective.Schema()), StructuralFingerprint: snapshot.Fingerprint(), Capabilities: effective.Capabilities(), CacheIdentity: "policy-v1"}}
	mux := http.NewServeMux()
	mux.Handle("/manifest", testManifestHandler("child", endpoint))
	var effectiveCalls atomic.Int32
	effectiveHandler := NewEffectiveSchemaHandler(endpoint, serverCodec)
	mux.Handle("/effective", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		effectiveCalls.Add(1)
		effectiveHandler.ServeHTTP(w, r)
	}))
	server := httptest.NewServer(mux)
	defer server.Close()
	remote, err := NewRemoteEndpoint(context.Background(), RemoteOptions{EndpointID: "child", ManifestURL: server.URL + "/manifest", EffectiveSchemaURL: server.URL + "/effective", ExecuteURL: server.URL + "/graphql", Delegation: clientCodec, EffectiveCacheTTL: 100 * time.Millisecond, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	remote.clock = func() time.Time { return now }
	identity, err := core.NewIdentity(core.IdentityInput{Tenant: "t", Subject: "u"})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := remote.EffectiveSchema(core.WithIdentity(context.Background(), identity))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.EffectiveSchema(core.WithIdentity(context.Background(), identity)); err != nil {
		t.Fatal(err)
	}
	for _, requestID := range []string{"request-1", "request-2"} {
		requestIdentity, err := core.NewIdentity(core.IdentityInput{Tenant: "t", Subject: "u", RequestID: requestID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := remote.EffectiveSchema(core.WithIdentity(context.Background(), requestIdentity)); err != nil {
			t.Fatal(err)
		}
	}
	if effectiveCalls.Load() != 1 {
		t.Fatalf("warm effective schema calls = %d, want 1", effectiveCalls.Load())
	}
	other, err := core.NewIdentity(core.IdentityInput{Tenant: "t", Subject: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.EffectiveSchema(core.WithIdentity(context.Background(), other)); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.EffectiveSchema(core.WithScope(core.WithIdentity(context.Background(), identity), map[string]any{"region": "cn"})); err != nil {
		t.Fatal(err)
	}
	if effectiveCalls.Load() != 3 {
		t.Fatalf("identity/scope invalidation calls = %d, want 3", effectiveCalls.Load())
	}
	changed := newArtifact(t, "type Query { changed: String }")
	endpoint.effective = core.SchemaArtifact{Schema: changed.Schema(), Fingerprint: schemaDigest(changed.Schema()), StructuralFingerprint: snapshot.Fingerprint(), Capabilities: changed.Capabilities(), CacheIdentity: "policy-v2"}
	now = now.Add(101 * time.Millisecond)
	refreshed, err := remote.EffectiveSchema(core.WithIdentity(context.Background(), identity))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(refreshed.Schema, "changed") || effectiveCalls.Load() != 4 {
		t.Fatalf("expired effective cache did not refresh: calls=%d schema=%s", effectiveCalls.Load(), refreshed.Schema)
	}
	uncacheable := newArtifact(t, "type Query { dynamic: String }")
	endpoint.effective = core.SchemaArtifact{Schema: uncacheable.Schema(), Fingerprint: schemaDigest(uncacheable.Schema()), StructuralFingerprint: snapshot.Fingerprint(), Capabilities: uncacheable.Capabilities()}
	now = now.Add(101 * time.Millisecond)
	for range 2 {
		if _, err := remote.EffectiveSchema(core.WithIdentity(context.Background(), identity)); err != nil {
			t.Fatal(err)
		}
	}
	if effectiveCalls.Load() != 6 {
		t.Fatalf("uncacheable effective artifact calls = %d, want 6", effectiveCalls.Load())
	}
	if artifact.Fingerprint == snapshot.Fingerprint() {
		t.Fatal("effective schema fell back to static manifest")
	}
	gateway, err := core.NewGateway("parent", core.Namespace{Name: "child", EndpointID: "child", Endpoint: remote})
	if err != nil {
		t.Fatal(err)
	}
	composed, err := gateway.EffectiveSchema(core.WithIdentity(context.Background(), identity))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(composed.Schema, "secret") {
		t.Fatalf("parent gateway leaked static child schema: %s", composed.Schema)
	}
}

func TestRemoteEffectiveSchemaPinsHistoricalStructuralSnapshot(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	serverCodec, _ := core.NewDelegationCodec(core.DelegationConfig{Issuer: "gateway", Audience: "child", Key: key, TTL: time.Minute, Replay: core.NewMemoryReplayStore(64)})
	clientCodec, _ := core.NewDelegationCodec(core.DelegationConfig{Issuer: "gateway", Audience: "child", Key: key, TTL: time.Minute, Replay: core.NewMemoryReplayStore(64)})
	oldSnapshot := newArtifact(t, "type Query { old: String }")
	newSnapshot := newArtifact(t, "type Query { current: String }")
	oldArtifact := core.FinalizeSchemaArtifact(core.SchemaArtifact{Schema: oldSnapshot.Schema(), Fingerprint: schemaDigest(oldSnapshot.Schema()), StructuralFingerprint: oldSnapshot.Fingerprint(), Capabilities: oldSnapshot.Capabilities()})
	currentArtifact := core.FinalizeSchemaArtifact(core.SchemaArtifact{Schema: newSnapshot.Schema(), Fingerprint: schemaDigest(newSnapshot.Schema()), StructuralFingerprint: newSnapshot.Fingerprint(), Capabilities: newSnapshot.Capabilities()})
	old := &runtimeEndpoint{snapshot: oldSnapshot, effective: oldArtifact}
	newer := &runtimeEndpoint{snapshot: newSnapshot, effective: currentArtifact}
	rolling := &rollingRuntime{current: old, history: map[string]*runtimeEndpoint{oldSnapshot.Fingerprint(): old, newSnapshot.Fingerprint(): newer}, revision: 1}
	mux := http.NewServeMux()
	mux.Handle("/manifest", NewManifestHandler("child", rolling, func(*http.Request) bool { return true }))
	mux.Handle("/effective", NewEffectiveSchemaHandler(rolling, serverCodec))
	server := httptest.NewServer(mux)
	defer server.Close()
	remote, err := NewRemoteEndpoint(context.Background(), RemoteOptions{EndpointID: "child", ManifestURL: server.URL + "/manifest", EffectiveSchemaURL: server.URL + "/effective", ExecuteURL: server.URL, Delegation: clientCodec, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	rolling.current, rolling.revision = newer, 2
	if err := remote.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	pinned, err := remote.ForSnapshot(oldSnapshot.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := core.NewIdentity(core.IdentityInput{Tenant: "t", Subject: "u"})
	artifact, err := pinned.(interface {
		EffectiveSchema(context.Context) (core.SchemaArtifact, error)
	}).EffectiveSchema(core.WithIdentity(context.Background(), identity))
	if err != nil {
		t.Fatal(err)
	}
	expected := oldArtifact
	expected.Identity = core.IdentityDescriptorFromIdentity(identity)
	expected = core.FinalizeSchemaArtifact(expected)
	if artifact.StructuralFingerprint != oldSnapshot.Fingerprint() || artifact.ExecutionPin != expected.ExecutionPin {
		t.Fatalf("historical artifact = %#v", artifact)
	}
}

func TestRemoteEndpointStaticManifestComposesWithoutEffectiveURL(t *testing.T) {
	snapshot := newArtifact(t, "type Query { hello: String }")
	endpoint := &runtimeEndpoint{snapshot: snapshot}
	server := httptest.NewServer(testManifestHandler("child", endpoint))
	defer server.Close()
	remote, err := NewRemoteEndpoint(context.Background(), RemoteOptions{EndpointID: "child", ManifestURL: server.URL, ExecuteURL: server.URL, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := core.NewGateway("parent", core.Namespace{Name: "child", EndpointID: "child", Endpoint: remote})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := gateway.EffectiveSchema(context.Background())
	if err != nil || !strings.Contains(artifact.Schema, "hello") {
		t.Fatalf("static remote effective schema = %#v, %v", artifact, err)
	}
}

func TestRemoteEndpointRefreshesRevisionWithStableFingerprint(t *testing.T) {
	endpoint := &versionedRuntime{runtimeEndpoint: &runtimeEndpoint{snapshot: newArtifact(t, "type Query { hello: String }")}, revision: 1}
	server := httptest.NewServer(testManifestHandler("child", endpoint))
	defer server.Close()
	remote, err := NewRemoteEndpoint(context.Background(), RemoteOptions{EndpointID: "child", ManifestURL: server.URL, ExecuteURL: server.URL, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	endpoint.revision = 2
	if err := remote.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state := remote.ControlState(); state.Revision != 2 {
		t.Fatalf("control revision = %d", state.Revision)
	}
}

func TestRemoteEndpointRejectsStaleManifestRevision(t *testing.T) {
	first := newArtifact(t, "type Query { first: String }")
	second := newArtifact(t, "type Query { second: String }")
	var revision uint64 = 2
	endpoint := &runtimeEndpoint{snapshot: first}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Manifest{Version: EndpointProtocolVersion, Revision: revision, EndpointID: "child", Schema: endpoint.snapshot.Schema(), Fingerprint: endpoint.snapshot.Fingerprint(), Capabilities: endpoint.snapshot.Capabilities()})
	}))
	defer server.Close()
	remote, err := NewRemoteEndpoint(context.Background(), RemoteOptions{EndpointID: "child", ManifestURL: server.URL, ExecuteURL: server.URL, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	endpoint.snapshot = second
	revision = 1
	if err := remote.Refresh(context.Background()); !errors.Is(err, core.ErrControlStale) {
		t.Fatalf("stale manifest revision error = %v", err)
	}
}

func TestRemoteEndpointRejectsChangedZeroRevisionManifest(t *testing.T) {
	first := newArtifact(t, "type Query { first: String }")
	second := newArtifact(t, "type Query { second: String }")
	endpoint := &runtimeEndpoint{snapshot: first}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Manifest{Version: EndpointProtocolVersion, EndpointID: "child", Schema: endpoint.snapshot.Schema(), Fingerprint: endpoint.snapshot.Fingerprint(), Capabilities: endpoint.snapshot.Capabilities()})
	}))
	defer server.Close()
	remote, err := NewRemoteEndpoint(context.Background(), RemoteOptions{EndpointID: "child", ManifestURL: server.URL, ExecuteURL: server.URL, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	endpoint.snapshot = second
	if err := remote.Refresh(context.Background()); !errors.Is(err, core.ErrControlConflict) {
		t.Fatalf("zero revision conflict error = %v", err)
	}
}

func TestRemoteEndpointRejectsMixedVersionExecutionResponse(t *testing.T) {
	snapshot := newArtifact(t, "type Query { hello: String }")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", responseType)
		w.Header().Set("GQL-Snapshot", "different-version")
		_, _ = w.Write([]byte(`{"data":{"hello":"wrong"}}`))
	}))
	defer server.Close()
	remote := &RemoteEndpoint{client: server.Client(), execute: server.URL, timeout: time.Second, maxResponse: 1024}
	body, err := remote.executeOperation(context.Background(), `{ hello }`, nil, "", snapshot.Fingerprint())
	if err == nil || body != nil {
		t.Fatalf("mixed-version response accepted: body=%s err=%v", body, err)
	}
}

func TestRemoteEndpointRejectsNonOKGraphQLExecutionResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", responseType)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"data":{"hello":"must not be accepted"}}`))
	}))
	defer server.Close()
	remote := &RemoteEndpoint{client: server.Client(), execute: server.URL, timeout: time.Second, maxResponse: 1024}
	body, err := remote.executeOperation(context.Background(), `{ hello }`, nil, "", "")
	if err == nil || body != nil {
		t.Fatalf("non-200 GraphQL response accepted: body=%s err=%v", body, err)
	}
}

func TestRemoteAdmissionSlotsRejectAndRecover(t *testing.T) {
	ctx := context.Background()
	slots := make(chan struct{}, 1)
	release, err := acquireRemoteSlot(ctx, slots, "execute")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acquireRemoteSlot(ctx, slots, "execute"); err == nil {
		t.Fatal("full remote capacity was accepted")
	} else {
		var problem *RemoteProblemError
		if !errors.As(err, &problem) || problem.Code != "REMOTE_OVERLOADED" || !problem.Retryable {
			t.Fatalf("overload error = %#v", err)
		}
	}
	release()
	release, err = acquireRemoteSlot(ctx, slots, "execute")
	if err != nil {
		t.Fatal("released capacity was not reusable:", err)
	}
	release()
}

func TestRemoteEndpointLoadsManifestAndDelegatesIdentity(t *testing.T) {
	snapshot := newArtifact(t, "type Query { hello: String! }")
	endpoint := &runtimeEndpoint{snapshot: snapshot}
	key := []byte("0123456789abcdef0123456789abcdef")
	serverCodec, err := core.NewDelegationCodec(core.DelegationConfig{Issuer: "gateway", Audience: "child", Key: key, TTL: time.Minute, Replay: core.NewMemoryReplayStore(64)})
	if err != nil {
		t.Fatal(err)
	}
	clientCodec, err := core.NewDelegationCodec(core.DelegationConfig{Issuer: "gateway", Audience: "child", Key: key, TTL: time.Minute, Replay: core.NewMemoryReplayStore(64)})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/manifest", testManifestHandler("child", endpoint))
	mux.Handle("/graphql", NewHandler(endpoint, Options{Delegation: serverCodec}))
	server := httptest.NewServer(mux)
	defer server.Close()

	remote, err := NewRemoteEndpoint(context.Background(), RemoteOptions{
		EndpointID: "child", ManifestURL: server.URL + "/manifest", ExecuteURL: server.URL + "/graphql",
		Delegation: clientCodec, AllowHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := remote.Snapshot()
	if err != nil || loaded.Fingerprint() != snapshot.Fingerprint() {
		t.Fatalf("snapshot = %#v, %v", loaded, err)
	}
	identity, err := core.NewIdentity(core.IdentityInput{Tenant: "tenant", Subject: "user", Roles: []string{"reader"}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := remote.Execute(core.WithIdentity(context.Background(), identity), `{ hello }`, nil)
	if err != nil || string(body) != `{"data":{"hello":"world"}}` {
		t.Fatalf("execute = %s, %v", body, err)
	}
	if endpoint.identity.Tenant() != "tenant" || endpoint.identity.Subject() != "user" {
		t.Fatalf("delegated identity = %#v", endpoint.identity)
	}
	_, err = remote.Execute(core.WithMutationCommitMetadata(core.WithIdentity(context.Background(), identity)), `{ hello }`, nil)
	if err != nil || !endpoint.commitMetadata {
		t.Fatalf("commit metadata context was not forwarded: %v", err)
	}
	gateway, err := core.NewGateway("root", core.Namespace{Name: "child", Endpoint: remote})
	if err != nil {
		t.Fatal(err)
	}
	body, err = gateway.Execute(core.WithIdentity(context.Background(), identity), `{ child { hello } }`, nil)
	if err != nil || string(body) != `{"data":{"child":{"hello":"world"}}}` {
		t.Fatalf("gateway execute = %s, %v", body, err)
	}
}

func TestRemoteEndpointHealthMatchesManifestSnapshot(t *testing.T) {
	snapshot := newArtifact(t, "type Query { hello: String }")
	endpoint := &healthyRuntime{runtimeEndpoint: &runtimeEndpoint{snapshot: snapshot}}
	mux := http.NewServeMux()
	mux.Handle("/manifest", testManifestHandler("child", endpoint))
	mux.Handle("/graphql", NewHandler(endpoint, Options{}))
	mux.Handle("/health", NewHealthHandler(endpoint))
	server := httptest.NewServer(mux)
	defer server.Close()
	remote, err := NewRemoteEndpoint(context.Background(), RemoteOptions{
		EndpointID: "child", ManifestURL: server.URL + "/manifest", ExecuteURL: server.URL + "/graphql",
		HealthURL: server.URL + "/health", AllowHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report, err := remote.Health(context.Background()); err != nil || !report.Ready || report.SnapshotFingerprint != snapshot.Fingerprint() {
		t.Fatalf("remote health = %#v, %v", report, err)
	}
}

func TestRemoteEndpointDecodesNotReadyHealthReport(t *testing.T) {
	snapshot := newArtifact(t, "type Query { hello: String }")
	manifestEndpoint := &healthyRuntime{runtimeEndpoint: &runtimeEndpoint{snapshot: snapshot}}
	report := core.HealthReport{
		Ready: false, SnapshotOK: true, SnapshotFingerprint: snapshot.Fingerprint(),
		ControlRevision: 7, RouteRevision: 8, SecurityRevision: 9,
		DatabaseOK: false, CDCConfigured: true, CDCConnected: false,
	}
	mux := http.NewServeMux()
	mux.Handle("/manifest", testManifestHandler("child", manifestEndpoint))
	mux.Handle("/graphql", NewHandler(manifestEndpoint, Options{}))
	mux.Handle("/health", NewHealthHandler(healthProbeFunc(func(context.Context) (core.HealthReport, error) { return report, nil })))
	server := httptest.NewServer(mux)
	defer server.Close()
	remote, err := NewRemoteEndpoint(context.Background(), RemoteOptions{
		EndpointID: "child", ManifestURL: server.URL + "/manifest", ExecuteURL: server.URL + "/graphql",
		HealthURL: server.URL + "/health", AllowHTTP: true,
	})
	require.NoError(t, err)

	got, err := remote.Health(context.Background())
	require.Equal(t, report.ControlRevision, got.ControlRevision)
	require.Equal(t, report.RouteRevision, got.RouteRevision)
	require.Equal(t, report.SecurityRevision, got.SecurityRevision)
	require.False(t, got.DatabaseOK)
	var problem *RemoteProblemError
	require.ErrorAs(t, err, &problem)
	require.Equal(t, "REMOTE_NOT_READY", problem.Code)
	require.True(t, problem.Retryable)
}

func TestRemoteEndpointRejectsUnknownHealthVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":2,"ready":true,"snapshotOk":true,"databaseOk":true}`))
	}))
	defer server.Close()
	remote := &RemoteEndpoint{
		health: server.URL, client: server.Client(), timeout: time.Second,
		maxResponse: 1 << 20, unarySlots: make(chan struct{}, 1),
	}
	_, err := remote.healthFor(context.Background(), "")
	var problem *RemoteProblemError
	require.ErrorAs(t, err, &problem)
	require.Equal(t, "UNSUPPORTED_HEALTH_VERSION", problem.Code)
	require.False(t, problem.Retryable)
}

func TestRemoteEndpointAcceptsLegacyHealthWithoutVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ready":true,"snapshotOk":true,"databaseOk":true,"cdcConfigured":true,"cdcState":"IdleValidated","cdcConnected":true}`))
	}))
	defer server.Close()
	remote := &RemoteEndpoint{health: server.URL, client: server.Client(), timeout: time.Second, maxResponse: 1 << 20, unarySlots: make(chan struct{}, 1)}
	report, err := remote.healthFor(context.Background(), "")
	require.NoError(t, err)
	require.Equal(t, "IdleValidated", report.CDCState)
	require.True(t, report.CDCConnected)
}

func TestRemoteEndpointRejectsHealthManifestWithoutHealthURL(t *testing.T) {
	snapshot := newArtifact(t, "type Query { hello: String }")
	endpoint := &healthyRuntime{runtimeEndpoint: &runtimeEndpoint{snapshot: snapshot}}
	server := httptest.NewServer(testManifestHandler("child", endpoint))
	defer server.Close()
	_, err := NewRemoteEndpoint(context.Background(), RemoteOptions{
		EndpointID: "child", ManifestURL: server.URL, ExecuteURL: server.URL + "/graphql", AllowHTTP: true,
	})
	if err == nil {
		t.Fatal("health manifest was accepted without HealthURL")
	}
}

func TestRemoteEndpointRefreshKeepsLastKnownGoodManifest(t *testing.T) {
	snapshot := newArtifact(t, "type Query { hello: String }")
	endpoint := &runtimeEndpoint{snapshot: snapshot}
	mux := http.NewServeMux()
	mux.Handle("/manifest", testManifestHandler("child", endpoint))
	server := httptest.NewServer(mux)
	defer server.Close()
	remote, err := NewRemoteEndpoint(context.Background(), RemoteOptions{
		EndpointID: "child", ManifestURL: server.URL + "/manifest", ExecuteURL: server.URL + "/graphql", AllowHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint.snapshot = newArtifact(t, "type Query { broken: String }")
	badMux := http.NewServeMux()
	badMux.Handle("/manifest", testManifestHandler("different-id", endpoint))
	badServer := httptest.NewServer(badMux)
	defer badServer.Close()
	remote.manifest = badServer.URL + "/manifest"
	if err := remote.Refresh(context.Background()); err == nil {
		t.Fatal("invalid manifest refresh succeeded")
	}
	got, _ := remote.Snapshot()
	if got.Fingerprint() != snapshot.Fingerprint() {
		t.Fatalf("LKG fingerprint = %s", got.Fingerprint())
	}
}

func TestRemoteEndpointRetainsRollingSnapshotWindow(t *testing.T) {
	first := newArtifact(t, "type Query { hello: String }")
	endpoint := &versionedRuntime{runtimeEndpoint: &runtimeEndpoint{snapshot: first}, revision: 1}
	server := httptest.NewServer(testManifestHandler("child", endpoint))
	defer server.Close()
	remote, err := NewRemoteEndpoint(context.Background(), RemoteOptions{
		EndpointID: "child", ManifestURL: server.URL, ExecuteURL: server.URL + "/graphql", AllowHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint.snapshot = newArtifact(t, "type Query { goodbye: String }")
	endpoint.revision = 2
	if err = remote.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	pinned, err := remote.ForSnapshot(first.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	got, err := pinned.Snapshot()
	if err != nil || got.Fingerprint() != first.Fingerprint() {
		t.Fatalf("rolling snapshot = %#v, %v", got, err)
	}
	artifact, err := pinned.(interface {
		EffectiveSchema(context.Context) (core.SchemaArtifact, error)
	}).EffectiveSchema(context.Background())
	if err != nil || artifact.StructuralFingerprint != first.Fingerprint() {
		t.Fatalf("pinned static effective schema = %#v, %v", artifact, err)
	}
	endpoint.snapshot = first
	endpoint.revision++
	if err = remote.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"c", "d", "e", "f", "g", "h", "i", "j"} {
		endpoint.snapshot = newArtifact(t, "type Query { "+field+": String }")
		endpoint.revision++
		if err = remote.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = remote.ForSnapshot(first.Fingerprint()); err != nil {
		t.Fatal("recently-current snapshot was evicted before older history:", err)
	}
}

func TestRemoteGatewaySubscriptionUsesSignedSSEStream(t *testing.T) {
	snapshot := newArtifact(t, `
		type Query { hello: String }
		type Subscription { changed: String! }
	`)
	frames := make(chan []byte, 1)
	frames <- []byte(`{"data":{"changed":"ready"}}`)
	close(frames)
	endpoint := &subscriptionRuntime{runtimeEndpoint: &runtimeEndpoint{snapshot: snapshot}, events: frames}
	key := []byte("0123456789abcdef0123456789abcdef")
	serverCodec, _ := core.NewDelegationCodec(core.DelegationConfig{Issuer: "gateway", Audience: "child", Key: key, TTL: time.Minute, Replay: core.NewMemoryReplayStore(64)})
	clientCodec, _ := core.NewDelegationCodec(core.DelegationConfig{Issuer: "gateway", Audience: "child", Key: key, TTL: time.Minute, Replay: core.NewMemoryReplayStore(64)})
	mux := http.NewServeMux()
	mux.Handle("/manifest", testManifestHandler("child", endpoint))
	mux.Handle("/graphql", NewHandler(endpoint, Options{Delegation: serverCodec}))
	mux.Handle("/subscribe", NewHandler(endpoint, Options{Delegation: serverCodec}))
	server := httptest.NewServer(mux)
	defer server.Close()
	remote, err := NewRemoteEndpoint(context.Background(), RemoteOptions{
		EndpointID: "child", ManifestURL: server.URL + "/manifest", ExecuteURL: server.URL + "/graphql",
		SubscribeURL: server.URL + "/subscribe", Delegation: clientCodec, AllowHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := core.NewGateway("root", core.Namespace{Name: "child", Endpoint: remote})
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := core.NewIdentity(core.IdentityInput{Tenant: "tenant", Subject: "subscriber"})
	events, err := gateway.Subscribe(core.WithIdentity(context.Background(), identity), `subscription { child { changed } }`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if body := <-events; string(body) != `{"data":{"child":{"changed":"ready"}}}` {
		t.Fatalf("subscription frame = %s", body)
	}
	if _, open := <-events; open {
		t.Fatal("completed remote subscription remained open")
	}
	if endpoint.identity.Subject() != "subscriber" {
		t.Fatalf("subscription identity = %#v", endpoint.identity)
	}
}

func TestRemoteEndpointRejectsSubscriptionManifestWithoutExecutableStream(t *testing.T) {
	snapshot := newArtifact(t, `
		type Query { hello: String }
		type Subscription { changed: String }
	`)
	frames := make(chan []byte)
	endpoint := &subscriptionRuntime{runtimeEndpoint: &runtimeEndpoint{snapshot: snapshot}, events: frames}
	mux := http.NewServeMux()
	mux.Handle("/manifest", testManifestHandler("child", endpoint))
	server := httptest.NewServer(mux)
	defer server.Close()

	_, err := NewRemoteEndpoint(context.Background(), RemoteOptions{
		EndpointID: "child", ManifestURL: server.URL + "/manifest", ExecuteURL: server.URL + "/graphql", AllowHTTP: true,
	})
	if err == nil {
		t.Fatal("subscription manifest was accepted without SubscribeURL")
	}
}

func TestRemoteDeclaresEffectiveSchemaCapabilityExplicitly(t *testing.T) {
	if (&RemoteEndpoint{}).EffectiveSchemaAvailable() || !(&RemoteEndpoint{effective: "https://child.test/effective"}).EffectiveSchemaAvailable() {
		t.Fatal("effective schema capability does not match endpoint configuration")
	}
}

func TestRemoteEndpointRejectsFalseSubscriptionCapability(t *testing.T) {
	snapshot := newArtifact(t, `
		type Query { hello: String }
		type Subscription { changed: String }
	`)
	endpoint := &runtimeEndpoint{snapshot: snapshot}
	mux := http.NewServeMux()
	mux.Handle("/manifest", testManifestHandler("child", endpoint))
	server := httptest.NewServer(mux)
	defer server.Close()

	_, err := NewRemoteEndpoint(context.Background(), RemoteOptions{
		EndpointID: "child", ManifestURL: server.URL + "/manifest", ExecuteURL: server.URL + "/graphql",
		SubscribeURL: server.URL + "/subscribe", AllowHTTP: true,
	})
	if err == nil {
		t.Fatal("schema-only subscription capability was accepted")
	}
}

func TestRemoteEndpointReconnectsTransientSSEEOFWithFixedSnapshot(t *testing.T) {
	snapshot := newArtifact(t, "type Subscription { changed: String }")
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("GQL-Snapshot"); got != snapshot.Fingerprint() {
			t.Errorf("snapshot = %q, want %q", got, snapshot.Fingerprint())
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("GQL-Snapshot", snapshot.Fingerprint())
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if attempts.Add(1) == 1 {
			_, _ = w.Write([]byte("event: next\ndata: {\"data\":{\"changed\":\"first\"}}\n\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return
		}
		_, _ = w.Write([]byte("event: next\ndata: {\"data\":{\"changed\":\"second\"}}\n\n"))
		_, _ = w.Write([]byte("event: complete\n\n"))
	}))
	defer server.Close()
	remote := &RemoteEndpoint{client: server.Client(), subscribe: server.URL, timeout: time.Second, maxResponse: 1024}
	events, err := remote.subscribeOperation(context.Background(), "subscription { changed }", nil, "", snapshot.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for frame := range events {
		got = append(got, string(frame))
	}
	if len(got) != 2 || !strings.Contains(got[0], "first") || !strings.Contains(got[1], "second") {
		t.Fatalf("events = %#v", got)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
}

func TestRemoteEndpointKeepsRetryingWhenReconnectHandshakeFails(t *testing.T) {
	snapshot := newArtifact(t, "type Subscription { changed: String }")
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch attempts.Add(1) {
		case 1:
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("GQL-Snapshot", snapshot.Fingerprint())
			_, _ = w.Write([]byte("event: next\ndata: {\"data\":{\"changed\":\"first\"}}\n\n"))
		case 2:
			http.Error(w, "temporary", http.StatusServiceUnavailable)
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("GQL-Snapshot", snapshot.Fingerprint())
			_, _ = w.Write([]byte("event: next\ndata: {\"data\":{\"changed\":\"recovered\"}}\n\nevent: complete\n\n"))
		}
	}))
	defer server.Close()
	remote := &RemoteEndpoint{client: server.Client(), subscribe: server.URL, timeout: time.Second, maxResponse: 1024}
	events, err := remote.subscribeOperation(context.Background(), "subscription { changed }", nil, "", snapshot.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	var frames []string
	for frame := range events {
		frames = append(frames, string(frame))
	}
	if len(frames) != 2 || !strings.Contains(frames[1], "recovered") || attempts.Load() != 3 {
		t.Fatalf("frames/attempts = %#v/%d", frames, attempts.Load())
	}
}

func TestRemoteEndpointResetsRetryBudgetAfterProgress(t *testing.T) {
	snapshot := newArtifact(t, "type Subscription { changed: String }")
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempt := attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("GQL-Snapshot", snapshot.Fingerprint())
		_, _ = fmt.Fprintf(w, "event: next\ndata: {\"data\":{\"changed\":%d}}\n\n", attempt)
		if attempt == 6 {
			_, _ = w.Write([]byte("event: complete\n\n"))
		}
	}))
	defer server.Close()
	remote := &RemoteEndpoint{client: server.Client(), subscribe: server.URL, timeout: time.Second, maxResponse: 1024}
	events, err := remote.subscribeOperation(context.Background(), "subscription { changed }", nil, "", snapshot.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	var frames int
	for range events {
		frames++
	}
	if frames != 6 || attempts.Load() != 6 {
		t.Fatalf("frames/attempts = %d/%d, want 6/6", frames, attempts.Load())
	}
}

func TestRemoteEndpointDoesNotRetryForeverAfterSuccessfulEmptySSEHandshake(t *testing.T) {
	snapshot := newArtifact(t, "type Subscription { changed: String }")
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("GQL-Snapshot", snapshot.Fingerprint())
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	remote := &RemoteEndpoint{client: server.Client(), subscribe: server.URL, timeout: time.Second, maxResponse: 1024}
	events, err := remote.subscribeOperation(ctx, "subscription { changed }", nil, "", snapshot.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	for frame := range events {
		if strings.Contains(string(frame), `"changed"`) {
			t.Fatal("empty SSE stream produced a data event")
		}
	}
	if got := attempts.Load(); got > 40 {
		t.Fatalf("empty successful handshakes retried %d times", got)
	}
}

func TestRemoteProblemPreservesCodeAtGraphQLBoundary(t *testing.T) {
	problem := &RemoteProblemError{Operation: "execute", Code: "SNAPSHOT_CHANGED", Message: "refresh", Retryable: true}
	errs := core.GraphQLErrors(problem)
	if len(errs) != 1 || errs[0].Extensions["code"] != "SNAPSHOT_CHANGED" {
		t.Fatalf("GraphQL errors lost remote problem code: %#v", errs)
	}
}

func TestRemoteEndpointRecoversAfterExtendedTransientOutageWithinWindow(t *testing.T) {
	snapshot := newArtifact(t, "type Subscription { changed: String }")
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempt := attempts.Add(1)
		if attempt == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("GQL-Snapshot", snapshot.Fingerprint())
			return
		}
		if attempt <= 6 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("GQL-Snapshot", snapshot.Fingerprint())
		_, _ = w.Write([]byte("event: next\ndata: {\"data\":{\"changed\":\"recovered\"}}\n\nevent: complete\n\n"))
	}))
	defer server.Close()
	remote := &RemoteEndpoint{client: server.Client(), subscribe: server.URL, timeout: time.Second, maxResponse: 1024, sseRecoveryWindow: time.Second, retryDelay: func(int) time.Duration { return 0 }}
	events, err := remote.subscribeOperation(context.Background(), "subscription { changed }", nil, "", snapshot.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for frame := range events {
		got = append(got, string(frame))
	}
	if len(got) != 1 || !strings.Contains(got[0], "recovered") || attempts.Load() != 7 {
		t.Fatalf("frames/attempts = %#v/%d", got, attempts.Load())
	}
}

func TestRemoteSubscriptionIdleWatchdogUnblocksRead(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	remote := &RemoteEndpoint{maxResponse: 1024, idleTimeout: 10 * time.Millisecond}
	done := make(chan error, 1)
	go func() {
		_, err, _ := remote.readSubscriptionProgress(context.Background(), reader, make(chan []byte, 1))
		done <- err
	}()
	select {
	case err := <-done:
		if !retryableReadError(err) {
			t.Fatalf("idle error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle SSE read was not released")
	}
}

func TestRemoteSubscriptionRejectsOversizedTokenWithoutRetry(t *testing.T) {
	remote := &RemoteEndpoint{maxResponse: 4096, idleTimeout: time.Second}
	_, err, _ := remote.readSubscriptionProgress(context.Background(), io.NopCloser(strings.NewReader("data: "+strings.Repeat("x", 5000)+"\n\n")), make(chan []byte, 1))
	if err == nil || retryableReadError(err) || !strings.Contains(err.Error(), "response limit") {
		t.Fatalf("oversized token error = %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestRemoteSubscriptionHandshakeTimeoutCancelsAttempt(t *testing.T) {
	canceled := make(chan struct{})
	remote := &RemoteEndpoint{
		client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			close(canceled)
			return nil, request.Context().Err()
		})},
		subscribe: "https://child.example/subscription", timeout: 10 * time.Millisecond,
	}
	_, err, _ := remote.openSubscription(context.Background(), request{}, []byte(`{}`), "")
	if err == nil {
		t.Fatal("handshake timeout succeeded")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("timed out handshake request was not canceled")
	}
}

func TestSubscriptionHandlerLimitsEventFrames(t *testing.T) {
	snapshot := newArtifact(t, "type Query { hello: String }")
	frames := make(chan []byte, 1)
	frames <- []byte(`{"data":{"value":"` + strings.Repeat("x", 64) + `"}}`)
	close(frames)
	endpoint := &subscriptionRuntime{runtimeEndpoint: &runtimeEndpoint{snapshot: snapshot}, events: frames}
	request := httptest.NewRequest(http.MethodPost, "/subscribe", strings.NewReader(`{"query":"subscription { changed }"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	response := &deadlineRecorder{httptest.NewRecorder()}
	NewHandler(endpoint, Options{MaxResponseBytes: 32}).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "LIMIT_EXCEEDED") || strings.Contains(response.Body.String(), strings.Repeat("x", 64)) {
		t.Fatalf("oversized subscription frame = %d %s", response.Code, response.Body.String())
	}
}

func TestEffectiveSchemaAcceptsImmutableEndpointAndRejectsStaleRevision(t *testing.T) {
	endpoint := &runtimeEndpoint{snapshot: newArtifact(t, "type Query { hello: String }")}
	// An immutable endpoint needs no revision-history wrapper.
	frozen := struct{ core.EffectiveSchemaEndpoint }{endpoint}
	handler := NewEffectiveSchemaHandler(frozen, acceptVerifier{})
	for _, revision := range []string{endpoint.snapshot.Fingerprint(), "stale"} {
		request := httptest.NewRequest(http.MethodGet, "/effective-schema", nil)
		request.Header.Set("Authorization", "GQL-Delegation test")
		request.Header.Set("GQL-Snapshot", revision)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if revision == "stale" {
			require.Equal(t, http.StatusConflict, response.Code)
			require.Contains(t, response.Body.String(), "SNAPSHOT_CHANGED")
		} else {
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		}
	}
}
