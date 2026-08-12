package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	core "github.com/ichaly/ideabase/gql"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

type subscriptionRevision struct {
	snapshot *core.Snapshot
	event    string
}

func (e *subscriptionRevision) Snapshot() (*core.Snapshot, error) { return e.snapshot, nil }
func (e *subscriptionRevision) Execute(context.Context, string, map[string]any) ([]byte, error) {
	return []byte(`{"data":{}}`), nil
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
func (e *pinningSubscription) ForSnapshot(string) (core.Endpoint, error) { return e.pinned, nil }

type subscriptionErrorStub struct{}

func (subscriptionErrorStub) Subscribe(context.Context, string, map[string]any, string) (<-chan []byte, error) {
	return nil, gqlerror.List{&gqlerror.Error{Message: "denied", Path: ast.Path{ast.PathName("users")}, Extensions: map[string]any{"code": "POLICY_DENIED", "reason": "role"}}}
}

func TestSubscriptionEstablishFailureKeepsGraphQLErrorMetadata(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/subscribe", strings.NewReader(`{"query":"subscription { users }"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "text/event-stream")
	w := httptest.NewRecorder()
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
