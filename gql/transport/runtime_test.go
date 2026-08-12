package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/ichaly/ideabase/gql"
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
			capabilities = append(capabilities, core.Capability{Coordinate: root.Name + "." + field.Name, Parent: root.Name, Name: field.Name, Type: field.Type.String(), Arguments: args, Handler: core.HandlerRemote})
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

func (e *runtimeEndpoint) EffectiveSchema(context.Context) (core.SchemaArtifact, error) {
	if e.effective.Schema != "" {
		return e.effective, nil
	}
	return core.SchemaArtifact{Schema: e.snapshot.Schema(), Fingerprint: schemaDigest(e.snapshot.Schema()), StructuralFingerprint: e.snapshot.Fingerprint(), Capabilities: e.snapshot.Capabilities()}, nil
}

type subscriptionRuntime struct {
	*runtimeEndpoint
	events <-chan []byte
}

type healthyRuntime struct{ *runtimeEndpoint }

type versionedRuntime struct {
	*runtimeEndpoint
	revision uint64
}

func (my *versionedRuntime) ManifestState() (*core.Snapshot, uint64, error) {
	return my.snapshot, my.revision, nil
}

func (my *healthyRuntime) Health(context.Context) (core.HealthReport, error) {
	return core.HealthReport{Ready: true, SnapshotOK: true, DatabaseOK: true, SnapshotFingerprint: my.snapshot.Fingerprint()}, nil
}

func (my *subscriptionRuntime) SubscriptionAvailable() bool { return true }
func (my *subscriptionRuntime) Subscribe(ctx context.Context, _ string, _ map[string]any, _ string) (<-chan []byte, error) {
	my.identity, _ = core.IdentityFromContext(ctx)
	return my.events, nil
}

func (my *runtimeEndpoint) Snapshot() (*core.Snapshot, error) { return my.snapshot, nil }
func (my *runtimeEndpoint) Execute(ctx context.Context, _ string, _ map[string]any) ([]byte, error) {
	my.identity, _ = core.IdentityFromContext(ctx)
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

func TestRemoteEndpointEffectiveSchemaUsesDelegatedIdentity(t *testing.T) {
	// The effective schema endpoint is intentionally identity-bound; a static
	// manifest must not be reused for a privileged caller's schema.
	snapshot := newArtifact(t, "type Query { public: String secret: String }")
	key := []byte("0123456789abcdef0123456789abcdef")
	serverCodec, _ := core.NewDelegationCodec(core.DelegationConfig{Issuer: "gateway", Audience: "child", Key: key, TTL: time.Minute, Replay: core.NewMemoryReplayStore(64)})
	clientCodec, _ := core.NewDelegationCodec(core.DelegationConfig{Issuer: "gateway", Audience: "child", Key: key, TTL: time.Minute, Replay: core.NewMemoryReplayStore(64)})
	effective := newArtifact(t, "type Query { public: String }")
	endpoint := &runtimeEndpoint{snapshot: snapshot, effective: core.SchemaArtifact{Schema: effective.Schema(), Fingerprint: schemaDigest(effective.Schema()), StructuralFingerprint: snapshot.Fingerprint(), Capabilities: effective.Capabilities()}}
	mux := http.NewServeMux()
	mux.Handle("/manifest", testManifestHandler("child", endpoint))
	mux.Handle("/effective", NewEffectiveSchemaHandler(endpoint, serverCodec))
	server := httptest.NewServer(mux)
	defer server.Close()
	remote, err := NewRemoteEndpoint(context.Background(), RemoteOptions{EndpointID: "child", ManifestURL: server.URL + "/manifest", EffectiveSchemaURL: server.URL + "/effective", ExecuteURL: server.URL + "/graphql", Delegation: clientCodec, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := core.NewIdentity(core.IdentityInput{Tenant: "t", Subject: "u"})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := remote.EffectiveSchema(core.WithIdentity(context.Background(), identity))
	if err != nil {
		t.Fatal(err)
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
		_ = json.NewEncoder(w).Encode(Manifest{Version: endpointProtocolVersion, Revision: revision, EndpointID: "child", Schema: endpoint.snapshot.Schema(), Fingerprint: endpoint.snapshot.Fingerprint(), Capabilities: endpoint.snapshot.Capabilities()})
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

func TestRemoteEndpointAcceptsChangedLegacyZeroRevisionManifest(t *testing.T) {
	first := newArtifact(t, "type Query { first: String }")
	second := newArtifact(t, "type Query { second: String }")
	endpoint := &runtimeEndpoint{snapshot: first}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Manifest{Version: endpointProtocolVersion, EndpointID: "child", Schema: endpoint.snapshot.Schema(), Fingerprint: endpoint.snapshot.Fingerprint(), Capabilities: endpoint.snapshot.Capabilities()})
	}))
	defer server.Close()
	remote, err := NewRemoteEndpoint(context.Background(), RemoteOptions{EndpointID: "child", ManifestURL: server.URL, ExecuteURL: server.URL, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	endpoint.snapshot = second
	if err := remote.Refresh(context.Background()); err != nil {
		t.Fatalf("legacy zero revision refresh error = %v", err)
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
	endpoint := &runtimeEndpoint{snapshot: first}
	server := httptest.NewServer(testManifestHandler("child", endpoint))
	defer server.Close()
	remote, err := NewRemoteEndpoint(context.Background(), RemoteOptions{
		EndpointID: "child", ManifestURL: server.URL, ExecuteURL: server.URL + "/graphql", AllowHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint.snapshot = newArtifact(t, "type Query { goodbye: String }")
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
	if err = remote.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"c", "d", "e", "f", "g", "h", "i", "j"} {
		endpoint.snapshot = newArtifact(t, "type Query { "+field+": String }")
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
	mux.Handle("/subscribe", NewSubscriptionHandler(endpoint, Options{Delegation: serverCodec}))
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

func TestSubscriptionHandlerLimitsEventFrames(t *testing.T) {
	snapshot := newArtifact(t, "type Query { hello: String }")
	frames := make(chan []byte, 1)
	frames <- []byte(`{"data":{"value":"` + strings.Repeat("x", 64) + `"}}`)
	close(frames)
	endpoint := &subscriptionRuntime{runtimeEndpoint: &runtimeEndpoint{snapshot: snapshot}, events: frames}
	request := httptest.NewRequest(http.MethodPost, "/subscribe", strings.NewReader(`{"query":"subscription { changed }"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	response := httptest.NewRecorder()
	NewSubscriptionHandler(endpoint, Options{MaxResponseBytes: 32}).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "LIMIT_EXCEEDED") || strings.Contains(response.Body.String(), strings.Repeat("x", 64)) {
		t.Fatalf("oversized subscription frame = %d %s", response.Code, response.Body.String())
	}
}
