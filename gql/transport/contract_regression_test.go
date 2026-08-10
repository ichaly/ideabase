package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/ichaly/ideabase/gql"
)

type contractEndpoint struct {
	name, query string
	body        []byte
}

type contractRevision struct {
	*contractEndpoint
	snapshot *core.Snapshot
}

func (e *contractRevision) Snapshot() (*core.Snapshot, error) { return e.snapshot, nil }

type pinningContract struct{ current, pinned *contractRevision }

func (e *pinningContract) Snapshot() (*core.Snapshot, error) { return e.current.Snapshot() }
func (e *pinningContract) ExecuteOperation(ctx context.Context, query string, vars map[string]any, name string) ([]byte, error) {
	return e.current.ExecuteOperation(ctx, query, vars, name)
}
func (e *pinningContract) ForSnapshot(string) (core.Endpoint, error) { return e.pinned, nil }

func (e *contractEndpoint) ExecuteOperation(_ context.Context, query string, _ map[string]any, name string) ([]byte, error) {
	e.query, e.name = query, name
	return e.body, nil
}
func (e *contractEndpoint) Execute(ctx context.Context, query string, vars map[string]any) ([]byte, error) {
	return e.ExecuteOperation(ctx, query, vars, "")
}

func TestContractBlankQueryForwardsOperationName(t *testing.T) {
	e := &contractEndpoint{body: []byte(`{"data":{}}`)}
	r := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"operationName":"Persisted"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", responseType)
	w := httptest.NewRecorder()
	NewHandler(e).ServeHTTP(w, r)
	if w.Code != http.StatusOK || e.query != "" || e.name != "Persisted" {
		t.Fatalf("status/query/name = %d/%q/%q", w.Code, e.query, e.name)
	}
}

func TestContractSnapshotHeaderExecutesPinnedRevision(t *testing.T) {
	old := &contractRevision{contractEndpoint: &contractEndpoint{body: []byte(`{"data":{"revision":"old"}}`)}, snapshot: newArtifact(t, "type Query { revision: String }")}
	newer := &contractRevision{contractEndpoint: &contractEndpoint{body: []byte(`{"data":{"revision":"new"}}`)}, snapshot: newArtifact(t, "type Query { revision: String! }")}
	r := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"{ revision }"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", responseType)
	r.Header.Set("GQL-Snapshot", old.snapshot.Fingerprint())
	w := httptest.NewRecorder()
	NewHandler(&pinningContract{current: newer, pinned: old}).ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"revision":"old"`) {
		t.Fatalf("status/body = %d/%s", w.Code, w.Body.String())
	}
}

func TestContractHealthRoundTripsAllFields(t *testing.T) {
	report := core.HealthReport{Ready: true, SnapshotOK: true, SnapshotFingerprint: "rev", DatabaseOK: false, DatabaseError: "db down", CDCConfigured: true, SubscriptionAvailable: true}
	w := httptest.NewRecorder()
	NewHealthHandler(healthStub{report: report}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	for _, want := range []string{`"databaseOk":false`, `"databaseError":"db down"`, `"cdcConfigured":true`, `"subscriptionAvailable":true`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("health body missing %s: %s", want, w.Body)
		}
	}
}

type healthStub struct{ report core.HealthReport }

func (h healthStub) Health(context.Context) (core.HealthReport, error) { return h.report, nil }

func TestContractSSELimitsCumulativeDataLines(t *testing.T) {
	events := make(chan []byte, 1)
	events <- []byte(`{"a":"1234"}
{"b":"5678"}`)
	close(events)
	r := httptest.NewRequest(http.MethodPost, "/subscribe", strings.NewReader(`{"query":"subscription { x }"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "text/event-stream")
	w := httptest.NewRecorder()
	NewSubscriptionHandler(subscriptionStub{events: events}, Options{MaxResponseBytes: 30}).ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "LIMIT_EXCEEDED") {
		t.Fatalf("event was not limited: %s", w.Body)
	}
}

type subscriptionStub struct{ events <-chan []byte }

func (s subscriptionStub) Subscribe(context.Context, string, map[string]any, string) (<-chan []byte, error) {
	return s.events, nil
}

func TestContractForSnapshotBindsCapabilitiesAtomically(t *testing.T) {
	first := newArtifact(t, "type Query { first: String }")
	second := newArtifact(t, "type Query { second: String }")
	remote := &RemoteEndpoint{}
	remote.state.Store(&remoteState{snapshot: first, subscriptionAvailable: true, healthAvailable: false})
	pinned, err := remote.ForSnapshot(first.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	remote.state.Store(&remoteState{snapshot: second, subscriptionAvailable: false, healthAvailable: true})
	pinnedSnapshot, _ := pinned.Snapshot()
	if pinnedSnapshot.Fingerprint() != first.Fingerprint() {
		t.Fatal("pinned snapshot changed after refresh")
	}
	pinnedSub, ok := pinned.(interface{ SubscriptionAvailable() bool })
	if !ok || !pinnedSub.SubscriptionAvailable() {
		t.Fatal("pinned subscription capability changed after refresh")
	}
}

func TestContractRemoteRejectsHTTPUnlessExplicitlyAllowed(t *testing.T) {
	_, err := NewRemoteEndpoint(context.Background(), RemoteOptions{EndpointID: "x", ManifestURL: "http://example.test/manifest", ExecuteURL: "http://example.test/graphql"})
	if err == nil {
		t.Fatal("insecure remote URL was accepted")
	}
}

func TestContractRemoteNeverAllowsCleartextToANetworkHost(t *testing.T) {
	if remoteURLAllowed("http://example.test/graphql", true) {
		t.Fatal("cleartext network remote was accepted")
	}
	if !remoteURLAllowed("http://127.0.0.1:8080/graphql", true) {
		t.Fatal("explicit loopback test endpoint was rejected")
	}
}

func TestContractSubscriptionHeaderTimeoutDoesNotCutStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(40 * time.Millisecond)
		_, _ = w.Write([]byte("event: next\ndata: "))
		_, _ = w.Write([]byte(`{"data":{}}` + "\n\n"))
	}))
	defer server.Close()
	remote := &RemoteEndpoint{client: server.Client(), subscribe: server.URL, timeout: 10 * time.Millisecond, maxResponse: 1024}
	events, err := remote.Subscribe(context.Background(), "subscription { x }", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := <-events; !ok {
		t.Fatal("stream was cut off by handshake timeout")
	}
}

func TestContractSubscriptionHeaderTimeoutRejectsDelayedHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(40 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	remote := &RemoteEndpoint{client: server.Client(), subscribe: server.URL, timeout: 10 * time.Millisecond, maxResponse: 1024}
	if _, err := remote.Subscribe(context.Background(), "subscription { x }", nil, ""); err == nil {
		t.Fatal("delayed headers were accepted")
	}
}

func TestContractRemoteSSELimitsWholeEventNotEachLine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: next\ndata: 1234567890\ndata: 1234567890\ndata: 1234567890\n\n"))
	}))
	defer server.Close()
	remote := &RemoteEndpoint{client: server.Client(), subscribe: server.URL, timeout: time.Second, maxResponse: 24}
	events, err := remote.Subscribe(context.Background(), "subscription { x }", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	frame, ok := <-events
	if !ok || !strings.Contains(string(frame), "response limit") || len(frame) > 128 {
		t.Fatalf("oversized event was forwarded: %q", frame)
	}
}
