package transport

import (
	"context"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type unifiedEndpoint struct{ executed, subscribed int }

func (e *unifiedEndpoint) ExecuteOperation(context.Context, string, map[string]any, string) ([]byte, error) {
	e.executed++
	return []byte(`{"data":{"value":1}}`), nil
}
func (e *unifiedEndpoint) Subscribe(context.Context, string, map[string]any, string) (<-chan []byte, error) {
	e.subscribed++
	ch := make(chan []byte, 1)
	ch <- []byte(`{"data":{"value":2}}`)
	close(ch)
	return ch, nil
}
func TestHandlerServesJSONAndSSEOnSamePath(t *testing.T) {
	endpoint := &unifiedEndpoint{}
	handler := NewHandler(endpoint, Options{})
	for _, tc := range []struct{ accept, query, content string }{{"application/json", "query { value }", "application/json"}, {"text/event-stream", "subscription { value }", "text/event-stream"}} {
		r := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"`+tc.query+`"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", tc.accept)
		w := &deadlineRecorder{httptest.NewRecorder()}
		handler.ServeHTTP(w, r)
		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, tc.content, w.Header().Get("Content-Type"))
	}
	require.Equal(t, 1, endpoint.executed)
	require.Equal(t, 1, endpoint.subscribed)
}

func (*blockingSubscriptionEndpoint) ExecuteOperation(context.Context, string, map[string]any, string) ([]byte, error) {
	return []byte(`{"data":{"value":1}}`), nil
}

func (subscriptionErrorStub) ExecuteOperation(context.Context, string, map[string]any, string) ([]byte, error) {
	return []byte(`{"data":{"value":1}}`), nil
}

func (blockingSubscription) ExecuteOperation(context.Context, string, map[string]any, string) ([]byte, error) {
	return []byte(`{"data":{"value":1}}`), nil
}

func (subscriptionStub) ExecuteOperation(context.Context, string, map[string]any, string) ([]byte, error) {
	return []byte(`{"data":{"value":1}}`), nil
}

func (*pinningSubscription) ExecuteOperation(context.Context, string, map[string]any, string) ([]byte, error) {
	return []byte(`{"data":{"value":1}}`), nil
}

func TestActiveStreamDoesNotBlockQueries(t *testing.T) {
	endpoint := &blockingSubscriptionEndpoint{entered: make(chan struct{}, 1), events: make(chan []byte)}
	handler := NewHandler(endpoint, Options{MaxConcurrent: 1, Timeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go func() {
		r := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"subscription { value }"}`)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "text/event-stream")
		handler.ServeHTTP(httptest.NewRecorder(), r)
		close(done)
	}()
	select {
	case <-endpoint.entered:
	case <-time.After(time.Second):
		t.Fatal("stream did not start")
	}
	r := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"{ value }"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"data":{"value":1}}`, w.Body.String())
}

func TestHandlerHonorsSSEQuality(t *testing.T) {
	for _, tc := range []struct {
		accept  string
		status  int
		streams int
	}{
		{"text/event-stream;q=0", http.StatusNotAcceptable, 0},
		{"text/event-stream;q=0.2, application/json;q=0.9", http.StatusOK, 0},
		{"text/event-stream;q=0.9, application/json;q=0.2", http.StatusOK, 1},
	} {
		t.Run(tc.accept, func(t *testing.T) {
			endpoint := &unifiedEndpoint{}
			r := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"{ value }"}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Accept", tc.accept)
			w := &deadlineRecorder{httptest.NewRecorder()}
			NewHandler(endpoint, Options{}).ServeHTTP(w, r)
			require.Equal(t, tc.status, w.Code)
			require.Equal(t, tc.streams, endpoint.subscribed)
		})
	}
}
