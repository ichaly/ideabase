package transport

import (
	"context"
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
	identity       core.Identity
	commitMetadata bool
	body           []byte
}

type subscriptionRuntime struct {
	*runtimeEndpoint
	events <-chan []byte
}

type healthyRuntime struct{ *runtimeEndpoint }

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
	mux.Handle("/manifest", NewManifestHandler("child", endpoint))
	mux.Handle("/graphql", NewHandler(endpoint))
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

func TestRemoteEndpointLoadsManifestAndDelegatesIdentity(t *testing.T) {
	snapshot := newArtifact(t, "type Query { hello: String! }")
	endpoint := &runtimeEndpoint{snapshot: snapshot}
	key := []byte("0123456789abcdef0123456789abcdef")
	serverCodec, err := core.NewDelegationCodec(core.DelegationConfig{Issuer: "gateway", Audience: "child", Key: key, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	clientCodec, err := core.NewDelegationCodec(core.DelegationConfig{Issuer: "gateway", Audience: "child", Key: key, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/manifest", NewManifestHandler("child", endpoint))
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
	mux.Handle("/manifest", NewManifestHandler("child", endpoint))
	mux.Handle("/graphql", NewHandler(endpoint))
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
	server := httptest.NewServer(NewManifestHandler("child", endpoint))
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
	mux.Handle("/manifest", NewManifestHandler("child", endpoint))
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
	badMux.Handle("/manifest", NewManifestHandler("different-id", endpoint))
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
	serverCodec, _ := core.NewDelegationCodec(core.DelegationConfig{Issuer: "gateway", Audience: "child", Key: key, TTL: time.Minute})
	clientCodec, _ := core.NewDelegationCodec(core.DelegationConfig{Issuer: "gateway", Audience: "child", Key: key, TTL: time.Minute})
	mux := http.NewServeMux()
	mux.Handle("/manifest", NewManifestHandler("child", endpoint))
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
	mux.Handle("/manifest", NewManifestHandler("child", endpoint))
	server := httptest.NewServer(mux)
	defer server.Close()

	_, err := NewRemoteEndpoint(context.Background(), RemoteOptions{
		EndpointID: "child", ManifestURL: server.URL + "/manifest", ExecuteURL: server.URL + "/graphql", AllowHTTP: true,
	})
	if err == nil {
		t.Fatal("subscription manifest was accepted without SubscribeURL")
	}
}

func TestRemoteEndpointRejectsFalseSubscriptionCapability(t *testing.T) {
	snapshot := newArtifact(t, `
		type Query { hello: String }
		type Subscription { changed: String }
	`)
	endpoint := &runtimeEndpoint{snapshot: snapshot}
	mux := http.NewServeMux()
	mux.Handle("/manifest", NewManifestHandler("child", endpoint))
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
