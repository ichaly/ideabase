package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/ichaly/ideabase/gql"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

type flushErrorWriter struct {
	header http.Header
	writes int
}

type heartbeatWriter struct {
	header http.Header
	wrote  chan string
	mu     sync.Mutex
}

type deadlineRecorder struct{ *httptest.ResponseRecorder }

func (*deadlineRecorder) SetWriteDeadline(time.Time) error { return nil }

type deadlineUnsupportedBlockingWriter struct {
	header http.Header
	block  <-chan struct{}
}

func (w *deadlineUnsupportedBlockingWriter) Header() http.Header { return w.header }
func (w *deadlineUnsupportedBlockingWriter) WriteHeader(int)     {}
func (w *deadlineUnsupportedBlockingWriter) Flush()              {}
func (w *deadlineUnsupportedBlockingWriter) Write([]byte) (int, error) {
	<-w.block
	return 0, errors.New("unblocked")
}

func TestSSEWriteReturnsWhenWriterCannotSetDeadline(t *testing.T) {
	block := make(chan struct{})
	w := &deadlineUnsupportedBlockingWriter{header: make(http.Header), block: block}
	done := make(chan error, 1)
	go func() { done <- writeSSEFrame(w, w, 10*time.Millisecond, []byte("event: next\n\n")) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("blocking writer unexpectedly succeeded")
		}
	case <-time.After(100 * time.Millisecond):
		close(block)
		t.Fatal("SSE write remained blocked without a supported deadline")
	}
	close(block)
}

func (w *heartbeatWriter) Header() http.Header              { return w.header }
func (w *heartbeatWriter) WriteHeader(int)                  {}
func (w *heartbeatWriter) Flush()                           {}
func (w *heartbeatWriter) SetWriteDeadline(time.Time) error { return nil }
func (w *heartbeatWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	select {
	case w.wrote <- string(p):
	default:
	}
	return len(p), nil
}

func (w *flushErrorWriter) Header() http.Header              { return w.header }
func (w *flushErrorWriter) WriteHeader(int)                  {}
func (w *flushErrorWriter) Write(p []byte) (int, error)      { w.writes++; return len(p), nil }
func (w *flushErrorWriter) Flush()                           {}
func (w *flushErrorWriter) FlushError() error                { return errors.New("client gone") }
func (w *flushErrorWriter) SetWriteDeadline(time.Time) error { return nil }

func TestSubscriptionHandlerStopsAfterFlushError(t *testing.T) {
	events := make(chan []byte, 2)
	events <- []byte(`{"data":{"value":"first"}}`)
	events <- []byte(`{"data":{"value":"second"}}`)
	close(events)
	w := &flushErrorWriter{header: make(http.Header)}
	r := httptest.NewRequest(http.MethodPost, "/subscribe", strings.NewReader(`{"query":"subscription { value }"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "text/event-stream")
	NewSubscriptionHandler(subscriptionStub{events: events}, Options{}).ServeHTTP(w, r)
	if w.writes != 1 {
		t.Fatalf("writes = %d, want 1 after FlushError", w.writes)
	}
}

func TestSubscriptionHandlerKeepsIdleStreamAlive(t *testing.T) {
	events := make(chan []byte)
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodPost, "/subscribe", strings.NewReader(`{"query":"subscription { value }"}`)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "text/event-stream")
	w := &heartbeatWriter{header: make(http.Header), wrote: make(chan string, 1)}
	done := make(chan struct{})
	go func() {
		NewSubscriptionHandler(subscriptionStub{events: events}, Options{Timeout: 20 * time.Millisecond}).ServeHTTP(w, r)
		close(done)
	}()
	select {
	case frame := <-w.wrote:
		if frame != ": keepalive\n\n" {
			t.Fatalf("heartbeat = %q", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("idle subscription emitted no heartbeat")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("subscription handler did not stop")
	}
}

type subscriptionRevision struct {
	snapshot *core.Snapshot
	event    string
}

func (e *subscriptionRevision) Snapshot() (*core.Snapshot, error) { return e.snapshot, nil }
func (e *subscriptionRevision) Execute(context.Context, string, map[string]any) ([]byte, error) {
	return []byte(`{"data":{}}`), nil
}
func (e *subscriptionRevision) ExecuteOperation(ctx context.Context, query string, variables map[string]any, _ string) ([]byte, error) {
	return e.Execute(ctx, query, variables)
}
func (e *subscriptionRevision) Subscribe(context.Context, string, map[string]any, string) (<-chan []byte, error) {
	events := make(chan []byte, 1)
	events <- []byte(e.event)
	close(events)
	return events, nil
}

type pinningSubscription struct{ current, pinned *subscriptionRevision }

func (e *pinningSubscription) Subscribe(ctx context.Context, query string, vars map[string]any, name string) (<-chan []byte, error) {
	return e.current.Subscribe(ctx, query, vars, name)
}
func (e *pinningSubscription) ForSnapshot(string) (core.ComposableEndpoint, error) {
	return e.pinned, nil
}

type effectiveSubscriptionRevision struct {
	*effectiveRevision
	ctx context.Context
}

func (e *effectiveSubscriptionRevision) Subscribe(ctx context.Context, _ string, _ map[string]any, _ string) (<-chan []byte, error) {
	e.ctx = ctx
	events := make(chan []byte, 1)
	events <- []byte(`{"data":{"x":"old"}}`)
	close(events)
	return events, nil
}

type effectiveSubscriptionHistory struct {
	current, pinned *effectiveSubscriptionRevision
}

func (e *effectiveSubscriptionHistory) Snapshot() (*core.Snapshot, error) {
	return e.current.snapshot, nil
}
func (e *effectiveSubscriptionHistory) ExecuteOperation(ctx context.Context, query string, variables map[string]any, name string) ([]byte, error) {
	return e.current.ExecuteOperation(ctx, query, variables, name)
}
func (e *effectiveSubscriptionHistory) Subscribe(ctx context.Context, query string, variables map[string]any, name string) (<-chan []byte, error) {
	return e.current.Subscribe(ctx, query, variables, name)
}
func (e *effectiveSubscriptionHistory) ForSnapshot(fingerprint string) (core.ComposableEndpoint, error) {
	if fingerprint != e.pinned.snapshot.Fingerprint() {
		return nil, fmt.Errorf("snapshot unavailable")
	}
	return e.pinned, nil
}

type subscriptionErrorStub struct{}

func (subscriptionErrorStub) Subscribe(context.Context, string, map[string]any, string) (<-chan []byte, error) {
	return nil, gqlerror.List{&gqlerror.Error{Message: "denied", Path: ast.Path{ast.PathName("users")}, Extensions: map[string]any{"code": "POLICY_DENIED", "reason": "role"}}}
}

func TestSubscriptionEstablishFailureKeepsGraphQLErrorMetadata(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/subscribe", strings.NewReader(`{"query":"subscription { users }"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "text/event-stream")
	w := &deadlineRecorder{httptest.NewRecorder()}
	NewSubscriptionHandler(subscriptionErrorStub{}, Options{}).ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"path":["users"]`) || !strings.Contains(w.Body.String(), `"code":"POLICY_DENIED"`) {
		t.Fatalf("status/body = %d/%s", w.Code, w.Body.String())
	}
}

func TestSubscriptionSnapshotHeaderRejectsHistoricalRevisionWithoutDelegation(t *testing.T) {
	old := &subscriptionRevision{snapshot: newArtifact(t, "type Query { x: String } type Subscription { x: String }"), event: `{"data":{"x":"old"}}`}
	newer := &subscriptionRevision{snapshot: newArtifact(t, "type Query { x: String! } type Subscription { x: String! }"), event: `{"data":{"x":"new"}}`}
	r := httptest.NewRequest(http.MethodPost, "/subscribe", strings.NewReader(`{"query":"subscription { x }"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "text/event-stream")
	r.Header.Set("GQL-Snapshot", old.snapshot.Fingerprint())
	w := httptest.NewRecorder()
	NewSubscriptionHandler(&pinningSubscription{current: newer, pinned: old}, Options{}).ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("status/body = %d/%s", w.Code, w.Body.String())
	}
}

func TestSubscriptionExecutionPinUsesHistoricalSnapshotView(t *testing.T) {
	oldSnapshot := newArtifact(t, "type Query { x: String } type Subscription { x: String }")
	currentSnapshot := newArtifact(t, "type Query { x: String! } type Subscription { x: String! }")
	artifact := func(snapshot *core.Snapshot) core.SchemaArtifact {
		return core.FinalizeSchemaArtifact(core.SchemaArtifact{Schema: snapshot.Schema(), Fingerprint: schemaDigest(snapshot.Schema()), StructuralFingerprint: snapshot.Fingerprint(), Capabilities: snapshot.Capabilities()})
	}
	old := &effectiveSubscriptionRevision{effectiveRevision: &effectiveRevision{snapshot: oldSnapshot, artifact: artifact(oldSnapshot)}}
	current := &effectiveSubscriptionRevision{effectiveRevision: &effectiveRevision{snapshot: currentSnapshot, artifact: artifact(currentSnapshot)}}
	handler := NewSubscriptionHandler(&effectiveSubscriptionHistory{current: current, pinned: old}, Options{Delegation: acceptVerifier{}})
	request := func(pin string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/subscribe", strings.NewReader(`{"query":"subscription { x }"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "text/event-stream")
		r.Header.Set("Authorization", delegationScheme+"token")
		r.Header.Set("GQL-Snapshot", oldSnapshot.Fingerprint())
		r.Header.Set(executionPinHeader, pin)
		return r
	}
	matching := &deadlineRecorder{httptest.NewRecorder()}
	handler.ServeHTTP(matching, request(old.artifact.ExecutionPin))
	if matching.Code != http.StatusOK || old.ctx == nil {
		t.Fatalf("historical subscription status/body = %d/%s", matching.Code, matching.Body.String())
	}
	mismatch := httptest.NewRecorder()
	handler.ServeHTTP(mismatch, request(current.artifact.ExecutionPin))
	if mismatch.Code != http.StatusConflict {
		t.Fatalf("mismatched pin status/body = %d/%s", mismatch.Code, mismatch.Body.String())
	}
}

func TestSubscriptionAcknowledgesPinnedSnapshot(t *testing.T) {
	snapshot := newArtifact(t, "type Query { x: String } type Subscription { x: String }")
	endpoint := &subscriptionRevision{snapshot: snapshot, event: `{"data":{"x":"ok"}}`}
	r := httptest.NewRequest(http.MethodPost, "/subscribe", strings.NewReader(`{"query":"subscription { x }"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "text/event-stream")
	r.Header.Set("GQL-Snapshot", snapshot.Fingerprint())
	w := httptest.NewRecorder()
	NewSubscriptionHandler(endpoint, Options{}).ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Header().Get("GQL-Snapshot") != snapshot.Fingerprint() {
		t.Fatalf("status/snapshot = %d/%q", w.Code, w.Header().Get("GQL-Snapshot"))
	}
}
