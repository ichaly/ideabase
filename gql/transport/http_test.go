package transport

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testEndpoint struct {
	query string
	vars  map[string]any
	ctx   context.Context
	body  []byte
	err   error
	name  string
}

func (e *testEndpoint) ExecuteOperation(ctx context.Context, query string, vars map[string]any, name string) ([]byte, error) {
	e.name = name
	return e.Execute(ctx, query, vars)
}

func (e *testEndpoint) Execute(ctx context.Context, query string, vars map[string]any) ([]byte, error) {
	e.ctx, e.query, e.vars = ctx, query, vars
	return e.body, e.err
}

func req(method, contentType, accept, body string) *http.Request {
	r := httptest.NewRequest(method, "/graphql", strings.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("Accept", accept)
	return r
}

func TestPOSTExecutesJSONWithNumberAndGraphQLResponse(t *testing.T) {
	ep := &testEndpoint{body: []byte(`{"data":{"ok":true}}`)}
	h := NewHandler(ep)
	r := req(http.MethodPost, "application/json", "application/graphql-response+json", `{"query":"query X($id: ID!) { thing }","operationName":"X","variables":{"id":9007199254740993}}`)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/graphql-response+json" {
		t.Fatalf("status/content-type = %d/%q", w.Code, w.Header().Get("Content-Type"))
	}
	if got := ep.vars["id"].(interface{ String() string }).String(); got != "9007199254740993" {
		t.Fatalf("number was not preserved: %s", got)
	}
	if ep.name != "X" {
		t.Fatalf("operationName was not forwarded: %q", ep.name)
	}
}

func TestLegacyJSONAndMissingAcceptRemainCompatible(t *testing.T) {
	for name, accept := range map[string]string{"json": "application/json", "wildcard": "*/*", "missing": ""} {
		t.Run(name, func(t *testing.T) {
			ep := &testEndpoint{body: []byte(`{"data":{"ok":true}}`)}
			w := httptest.NewRecorder()
			NewHandler(ep).ServeHTTP(w, req(http.MethodPost, "application/json", accept, `{"query":"{x}"}`))
			if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("legacy response = %d/%q", w.Code, w.Header().Get("Content-Type"))
			}
		})
	}

	ep := &testEndpoint{body: []byte(`{"errors":[{"message":"invalid"}]}`)}
	w := httptest.NewRecorder()
	NewHandler(ep).ServeHTTP(w, req(http.MethodPost, "application/json", "application/json", `{"query":"{x}"}`))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("legacy request error = %d/%q", w.Code, w.Header().Get("Content-Type"))
	}
}

func TestPOSTRejectsTrailingJSONAndWrongMediaTypes(t *testing.T) {
	for name, r := range map[string]*http.Request{
		"trailing":     req(http.MethodPost, "application/json", "application/graphql-response+json", `{"query":"{x}"} {}`),
		"content type": req(http.MethodPost, "text/plain", "application/graphql-response+json", `{"query":"{x}"}`),
		"charset":      req(http.MethodPost, "application/json;charset=iso-8859-1", "application/graphql-response+json", `{"query":"{x}"}`),
		"accept":       req(http.MethodPost, "application/json", "text/plain", `{"query":"{x}"}`),
		"zero quality": req(http.MethodPost, "application/json", "application/graphql-response+json;q=0.00", `{"query":"{x}"}`),
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			NewHandler(&testEndpoint{}).ServeHTTP(w, r)
			if w.Code < 400 || w.Code >= 500 {
				t.Fatalf("status = %d", w.Code)
			}
		})
	}
}

func TestRejectsInvalidEndpointGraphQLResponse(t *testing.T) {
	for _, body := range []string{`not-json`, `{}`, `{"data":null}`, `{"errors":[]}`} {
		w := httptest.NewRecorder()
		NewHandler(&testEndpoint{body: []byte(body)}).ServeHTTP(w, req(http.MethodPost, "application/json", "application/graphql-response+json", `{"query":"{x}"}`))
		if w.Code != http.StatusInternalServerError || w.Header().Get("Content-Type") == responseType {
			t.Fatalf("invalid endpoint response %q = %d/%q", body, w.Code, w.Header().Get("Content-Type"))
		}
	}
}

func TestGETAllowsQueryButRejectsMutation(t *testing.T) {
	ep := &testEndpoint{body: []byte(`{"data":{}}`)}
	h := NewHandler(ep)
	query := httptest.NewRequest(http.MethodGet, "/graphql?query=%7Bhello%7D", nil)
	query.Header.Set("Accept", "application/graphql-response+json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, query)
	if w.Code != http.StatusOK || ep.query != "{hello}" {
		t.Fatalf("query status/body = %d/%q", w.Code, ep.query)
	}
	mutation := httptest.NewRequest(http.MethodGet, "/graphql?query=mutation%20%7Bcreate%7D", nil)
	mutation.Header.Set("Accept", "application/graphql-response+json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, mutation)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("mutation status = %d", w.Code)
	}
	subscription := httptest.NewRequest(http.MethodGet, "/graphql?query=subscription%20%7Bchanged%7D", nil)
	subscription.Header.Set("Accept", "application/graphql-response+json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, subscription)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("subscription status = %d", w.Code)
	}
	persisted := httptest.NewRequest(http.MethodGet, "/graphql?operationName=PersistedMutation", nil)
	persisted.Header.Set("Accept", "application/graphql-response+json")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, persisted)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("persisted operation status = %d", w.Code)
	}
}

func TestRequestErrorUses422AndKeepsGraphQLBody(t *testing.T) {
	ep := &testEndpoint{body: []byte(`{"errors":[{"message":"failed"}]}`), err: io.ErrUnexpectedEOF}
	w := httptest.NewRecorder()
	NewHandler(ep).ServeHTTP(w, req(http.MethodPost, "application/json", "application/graphql-response+json", `{"query":"{x}"}`))
	if w.Code != http.StatusUnprocessableEntity || w.Body.String() != string(ep.body) {
		t.Fatalf("response = %d %s", w.Code, w.Body)
	}
}

func TestPartialExecutionUses200AndKeepsGraphQLBody(t *testing.T) {
	ep := &testEndpoint{body: []byte(`{"data":null,"errors":[{"message":"failed"}]}`)}
	w := httptest.NewRecorder()
	NewHandler(ep).ServeHTTP(w, req(http.MethodPost, "application/json", "application/graphql-response+json", `{"query":"{x}"}`))
	if w.Code != http.StatusOK || w.Body.String() != string(ep.body) {
		t.Fatalf("response = %d %s", w.Code, w.Body)
	}
}

func TestMalformedDocumentAndRequestShapeFailBeforeExecution(t *testing.T) {
	tests := []struct {
		name, body string
		status     int
	}{
		{name: "document syntax", body: `{"query":"{"}`, status: http.StatusBadRequest},
		{name: "missing query", body: `{}`, status: http.StatusUnprocessableEntity},
		{name: "variables shape", body: `{"query":"{x}","variables":[]}`, status: http.StatusUnprocessableEntity},
		{name: "ambiguous operation", body: `{"query":"query A{x} query B{x}"}`, status: http.StatusUnprocessableEntity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ep := &testEndpoint{}
			w := httptest.NewRecorder()
			NewHandler(ep).ServeHTTP(w, req(http.MethodPost, "application/json", "application/graphql-response+json", test.body))
			if w.Code != test.status || ep.ctx != nil {
				t.Fatalf("response/called = %d/%v", w.Code, ep.ctx != nil)
			}
		})
	}
}

func TestRequestBodyLimitAndDeadline(t *testing.T) {
	ep := &testEndpoint{body: []byte(`{"data":{}}`)}
	h := NewHandler(ep, Options{MaxBodyBytes: 8, Timeout: time.Second})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(http.MethodPost, "application/json", "application/graphql-response+json", `{"query":"{x}"}`))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body status = %d", w.Code)
	}
	ep = &testEndpoint{body: []byte(`{"data":{}}`)}
	h = NewHandler(ep, Options{Timeout: time.Millisecond})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(http.MethodPost, "application/json", "application/graphql-response+json", `{"query":"{x}"}`))
	if ep.ctx == nil {
		t.Fatal("endpoint did not receive context")
	}
	if _, ok := ep.ctx.Deadline(); !ok {
		t.Fatal("request deadline missing")
	}
}

func TestResponseBodyLimitRejectsBeforeWritingGraphQL(t *testing.T) {
	ep := &testEndpoint{body: []byte(`{"data":{"value":"` + strings.Repeat("x", 64) + `"}}`)}
	w := httptest.NewRecorder()
	NewHandler(ep, Options{MaxResponseBytes: 32}).ServeHTTP(w, req(http.MethodPost, "application/json", "application/graphql-response+json", `{"query":"{x}"}`))
	if w.Code != http.StatusInternalServerError || w.Header().Get("Content-Type") == responseType || bytes.Contains(w.Body.Bytes(), ep.body) {
		t.Fatalf("oversized response = %d/%q %s", w.Code, w.Header().Get("Content-Type"), w.Body.Bytes())
	}
}
