package transport

// This opt-in test exercises the complete cross-process control and data path:
// manifest, delegated effective schema, delegated GraphQL execution, health,
// and a state change observed by an already connected RemoteEndpoint.

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/ichaly/ideabase/gql"
	"github.com/stretchr/testify/require"
)

const gaHTTPChildEnv = "GQL_GA_HTTP_CHILD"

type gaHTTPChildEndpoint struct {
	*runtimeEndpoint
	disabled atomic.Bool
}

func (e *gaHTTPChildEndpoint) ExecuteOperation(ctx context.Context, query string, vars map[string]any, operation string) ([]byte, error) {
	if e.disabled.Load() {
		return []byte(`{"data":null,"errors":[{"message":"tenant disabled","extensions":{"code":"TENANT_DISABLED"}}]}`), nil
	}
	return e.runtimeEndpoint.ExecuteOperation(ctx, query, vars, operation)
}

func (e *gaHTTPChildEndpoint) EffectiveSchema(context.Context) (core.SchemaArtifact, error) {
	if e.disabled.Load() {
		return core.SchemaArtifact{}, fmt.Errorf("tenant disabled")
	}
	return e.runtimeEndpoint.EffectiveSchema(context.Background())
}
func (e *gaHTTPChildEndpoint) Health(context.Context) (core.HealthReport, error) {
	if e.disabled.Load() {
		return core.HealthReport{DatabaseOK: true, SnapshotOK: true, SnapshotFingerprint: e.snapshot.Fingerprint()}, nil
	}
	return core.HealthReport{DatabaseOK: true, SnapshotOK: true, SnapshotFingerprint: e.snapshot.Fingerprint(), Ready: true}, nil
}

func TestGAHTTPChild(t *testing.T) {
	if os.Getenv(gaHTTPChildEnv) == "" {
		return
	}
	snapshot := newArtifact(t, "type Query { hello: String! }")
	endpoint := &gaHTTPChildEndpoint{runtimeEndpoint: &runtimeEndpoint{snapshot: snapshot, body: []byte(`{"data":{"hello":"world"}}`)}}
	key := []byte("0123456789abcdef0123456789abcdef")
	codec, err := core.NewDelegationCodec(core.DelegationConfig{Issuer: "ga-gateway", Audience: "ga-child", Key: key, Replay: core.NewMemoryReplayStore(256)})
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle("/manifest", NewManifestHandler("ga-child", endpoint, func(*http.Request) bool { return true }))
	mux.Handle("/effective", NewEffectiveSchemaHandler(endpoint, codec))
	mux.Handle("/graphql", NewHandler(endpoint, Options{Delegation: codec}))
	mux.Handle("/health", NewHealthHandler(endpoint))
	mux.HandleFunc("/revoke", func(http.ResponseWriter, *http.Request) {
		endpoint.disabled.Store(true)
		endpoint.runtimeEndpoint.body = []byte(`{"data":null,"errors":[{"message":"tenant disabled","extensions":{"code":"TENANT_DISABLED"}}]}`)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	fmt.Printf("ready:%s\n", listener.Addr())
	_ = http.Serve(listener, mux)
}

func TestGAHTTPRemoteGatewayRoundTripAndStateChange(t *testing.T) {
	if os.Getenv("GQL_RUN_CLUSTER_GA") != "1" {
		t.Skip("set GQL_RUN_CLUSTER_GA=1 to run real multi-process HTTP GA")
	}
	childURL, stop := startGAHTTPChild(t)
	defer stop()
	key := []byte("0123456789abcdef0123456789abcdef")
	identity, err := core.NewIdentity(core.IdentityInput{Tenant: "ga-tenant", Subject: "worker"})
	require.NoError(t, err)
	ctx := core.WithIdentity(context.Background(), identity)
	codec, err := core.NewDelegationCodec(core.DelegationConfig{Issuer: "ga-gateway", Audience: "ga-child", Key: key, Replay: core.NewMemoryReplayStore(256)})
	require.NoError(t, err)
	remote, err := NewRemoteEndpoint(ctx, RemoteOptions{
		EndpointID: "ga-child", ManifestURL: childURL + "/manifest", EffectiveSchemaURL: childURL + "/effective",
		ExecuteURL: childURL + "/graphql", HealthURL: childURL + "/health", Delegation: codec, AllowHTTP: true, EffectiveCacheTTL: time.Millisecond,
	})
	require.NoError(t, err)
	gateway, err := core.NewGateway("ga-root", core.Namespace{Name: "child", EndpointID: "ga-child", Endpoint: remote})
	require.NoError(t, err)
	report, err := remote.Health(ctx)
	require.NoError(t, err)
	require.True(t, report.Ready)
	handler := NewHandler(gateway, Options{})
	request := func(requestCtx context.Context) string {
		r, err := http.NewRequestWithContext(requestCtx, http.MethodPost, "http://gateway/graphql", strings.NewReader(`{"query":"{ child { hello } }"}`))
		require.NoError(t, err)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/graphql-response+json")
		w := &responseCapture{header: make(http.Header)}
		handler.ServeHTTP(w, r)
		require.Equal(t, http.StatusOK, w.status, string(w.body))
		return string(w.body)
	}
	require.Contains(t, request(ctx), `"hello":"world"`)
	resp, err := http.Get(childURL + "/revoke")
	require.NoError(t, err)
	_ = resp.Body.Close()
	resp, err = http.Get(childURL + "/health")
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	_ = resp.Body.Close()
	time.Sleep(5 * time.Millisecond)
	newIdentity, err := core.NewIdentity(core.IdentityInput{Tenant: "ga-tenant", Subject: "worker-2"})
	require.NoError(t, err)
	direct, err := remote.ExecuteOperation(core.WithIdentity(context.Background(), newIdentity), `{ hello }`, nil, "")
	require.NoError(t, err)
	require.Contains(t, string(direct), "TENANT_DISABLED")
	require.Contains(t, request(core.WithIdentity(context.Background(), newIdentity)), "BAD_GATEWAY_RESPONSE")
}

type responseCapture struct {
	header http.Header
	status int
	body   []byte
}

func (w *responseCapture) Header() http.Header    { return w.header }
func (w *responseCapture) WriteHeader(status int) { w.status = status }
func (w *responseCapture) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.body = append(w.body, body...)
	return len(body), nil
}

func startGAHTTPChild(t *testing.T) (string, func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestGAHTTPChild", "-test.count=1")
	cmd.Env = append(os.Environ(), gaHTTPChildEnv+"=1")
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	scanner := bufio.NewScanner(stdout)
	ready := make(chan string, 1)
	go func() {
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "ready:") {
				ready <- "http://" + strings.TrimPrefix(scanner.Text(), "ready:")
				return
			}
		}
		close(ready)
	}()
	select {
	case url, ok := <-ready:
		require.True(t, ok, "HTTP child exited before ready")
		return url, func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("HTTP child did not become ready")
		return "", func() {}
	}
}
