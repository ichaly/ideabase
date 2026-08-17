package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/ichaly/ideabase/gql"
)

func requireTransportError(t *testing.T, w *httptest.ResponseRecorder, code string) {
	t.Helper()
	if got := w.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("content type = %q", got)
	}
	var body struct {
		Error struct {
			Code      string `json:"code"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.Error.Code != code {
		t.Fatalf("error code = %q, want %q", body.Error.Code, code)
	}
}

func TestTransportConstructorsHaveOneOptionsContract(t *testing.T) {
	if reflect.TypeOf(NewHandler).IsVariadic() || reflect.TypeOf(NewSubscriptionHandler).IsVariadic() {
		t.Fatal("transport constructors must not silently accept multiple option sets")
	}
}

type testEndpoint struct {
	query string
	vars  map[string]any
	ctx   context.Context
	body  []byte
	err   error
	name  string
}

type blockingEndpoint struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (e *blockingEndpoint) ExecuteOperation(ctx context.Context, _ string, _ map[string]any, _ string) ([]byte, error) {
	e.calls.Add(1)
	e.entered <- struct{}{}
	select {
	case <-e.release:
		return []byte(`{"data":{"ok":true}}`), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type signalingReader struct {
	reader io.Reader
	read   chan struct{}
}

func (r *signalingReader) Read(p []byte) (int, error) {
	select {
	case r.read <- struct{}{}:
	default:
	}
	return r.reader.Read(p)
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("overloaded request body was read") }

type effectivePinEndpoint struct {
	testEndpoint
	artifact core.SchemaArtifact
}

type acceptVerifier struct{}

func (acceptVerifier) Verify(ctx context.Context, _, _ string) (context.Context, error) {
	return ctx, nil
}

type effectiveRevision struct {
	testEndpoint
	snapshot *core.Snapshot
	artifact core.SchemaArtifact
}

func (e *effectiveRevision) Snapshot() (*core.Snapshot, error) { return e.snapshot, nil }
func (e *effectiveRevision) EffectiveSchema(context.Context) (core.SchemaArtifact, error) {
	return e.artifact, nil
}

type effectiveHistory struct{ current, pinned *effectiveRevision }

func (e *effectiveHistory) Snapshot() (*core.Snapshot, error) { return e.current.snapshot, nil }
func (e *effectiveHistory) ExecuteOperation(ctx context.Context, query string, variables map[string]any, operationName string) ([]byte, error) {
	return e.current.ExecuteOperation(ctx, query, variables, operationName)
}
func (e *effectiveHistory) ForSnapshot(fingerprint string) (core.ComposableEndpoint, error) {
	if fingerprint != e.pinned.snapshot.Fingerprint() {
		return nil, fmt.Errorf("snapshot unavailable")
	}
	return e.pinned, nil
}

func (e *effectivePinEndpoint) EffectiveSchema(context.Context) (core.SchemaArtifact, error) {
	return e.artifact, nil
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
	h := NewHandler(ep, Options{})
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

func TestHTTPExecutionPinHeaderIsValidatedWhenPresent(t *testing.T) {
	ep := &effectivePinEndpoint{testEndpoint: testEndpoint{body: []byte(`{"data":{"ok":true}}`)}, artifact: core.SchemaArtifact{ExecutionPin: "pin"}}
	h := NewHandler(ep, Options{})
	for _, tc := range []struct {
		name, pin    string
		status, code int
	}{
		{name: "matching", pin: "pin", status: http.StatusOK},
		{name: "mismatch", pin: "stale", status: http.StatusConflict, code: 1},
		{name: "direct client", status: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := req(http.MethodPost, "application/json", "application/graphql-response+json", `{"query":"{x}"}`)
			if tc.pin != "" {
				r.Header.Set("GQL-Execution-Pin", tc.pin)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			if tc.code != 0 {
				requireTransportError(t, w, "SNAPSHOT_CONFLICT")
			}
			if tc.name == "matching" {
				if pin, ok := core.ExecutionPinFromContext(ep.ctx); !ok || pin != tc.pin {
					t.Fatalf("execution context pin = %q/%t", pin, ok)
				}
			}
		})
	}
}

func TestHTTPExecutionPinUsesHistoricalSnapshotView(t *testing.T) {
	oldSnapshot := newArtifact(t, "type Query { x: String }")
	currentSnapshot := newArtifact(t, "type Query { x: String! }")
	artifact := func(snapshot *core.Snapshot) core.SchemaArtifact {
		return core.FinalizeSchemaArtifact(core.SchemaArtifact{Schema: snapshot.Schema(), Fingerprint: schemaDigest(snapshot.Schema()), StructuralFingerprint: snapshot.Fingerprint(), Capabilities: snapshot.Capabilities()})
	}
	old := &effectiveRevision{testEndpoint: testEndpoint{body: []byte(`{"data":{"x":"old"}}`)}, snapshot: oldSnapshot, artifact: artifact(oldSnapshot)}
	current := &effectiveRevision{testEndpoint: testEndpoint{body: []byte(`{"data":{"x":"new"}}`)}, snapshot: currentSnapshot, artifact: artifact(currentSnapshot)}
	handler := NewHandler(&effectiveHistory{current: current, pinned: old}, Options{Delegation: acceptVerifier{}})
	request := func(pin string) *http.Request {
		r := req(http.MethodPost, "application/json", responseType, `{"query":"{x}"}`)
		r.Header.Set("Authorization", delegationScheme+"token")
		r.Header.Set("GQL-Snapshot", oldSnapshot.Fingerprint())
		r.Header.Set(executionPinHeader, pin)
		return r
	}
	matching := httptest.NewRecorder()
	handler.ServeHTTP(matching, request(old.artifact.ExecutionPin))
	if matching.Code != http.StatusOK || old.ctx == nil {
		t.Fatalf("historical execution status/body = %d/%s", matching.Code, matching.Body.String())
	}
	mismatch := httptest.NewRecorder()
	handler.ServeHTTP(mismatch, request(current.artifact.ExecutionPin))
	if mismatch.Code != http.StatusConflict {
		t.Fatalf("mismatched pin status/body = %d/%s", mismatch.Code, mismatch.Body.String())
	}
}

func TestLegacyJSONAndMissingAcceptRemainCompatible(t *testing.T) {
	for name, accept := range map[string]string{"json": "application/json", "wildcard": "*/*", "missing": ""} {
		t.Run(name, func(t *testing.T) {
			ep := &testEndpoint{body: []byte(`{"data":{"ok":true}}`)}
			w := httptest.NewRecorder()
			NewHandler(ep, Options{}).ServeHTTP(w, req(http.MethodPost, "application/json", accept, `{"query":"{x}"}`))
			if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("legacy response = %d/%q", w.Code, w.Header().Get("Content-Type"))
			}
		})
	}

	ep := &testEndpoint{body: []byte(`{"errors":[{"message":"invalid"}]}`)}
	w := httptest.NewRecorder()
	NewHandler(ep, Options{}).ServeHTTP(w, req(http.MethodPost, "application/json", "application/json", `{"query":"{x}"}`))
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
			NewHandler(&testEndpoint{}, Options{}).ServeHTTP(w, r)
			if w.Code < 400 || w.Code >= 500 {
				t.Fatalf("status = %d", w.Code)
			}
		})
	}
}

func TestTransportFailuresUseStableJSONCodes(t *testing.T) {
	tests := []struct {
		name, code string
		request    *http.Request
	}{
		{name: "accept", code: "NOT_ACCEPTABLE", request: req(http.MethodPost, "application/json", "text/plain", `{}`)},
		{name: "content type", code: "UNSUPPORTED_MEDIA_TYPE", request: req(http.MethodPost, "text/plain", "application/json", `{}`)},
		{name: "method", code: "METHOD_NOT_ALLOWED", request: req(http.MethodPut, "application/json", "application/json", `{}`)},
		{name: "missing query", code: "INVALID_REQUEST", request: req(http.MethodPost, "application/json", "application/json", `{}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			NewHandler(&testEndpoint{}, Options{}).ServeHTTP(w, test.request)
			requireTransportError(t, w, test.code)
		})
	}
}

func TestRejectsInvalidEndpointGraphQLResponse(t *testing.T) {
	for _, body := range []string{`not-json`, `{}`, `{"data":null}`, `{"errors":[]}`} {
		w := httptest.NewRecorder()
		NewHandler(&testEndpoint{body: []byte(body)}, Options{}).ServeHTTP(w, req(http.MethodPost, "application/json", "application/graphql-response+json", `{"query":"{x}"}`))
		if w.Code != http.StatusInternalServerError || w.Header().Get("Content-Type") != responseType || !bytes.Contains(w.Body.Bytes(), []byte(`"code":"BAD_GATEWAY_RESPONSE"`)) {
			t.Fatalf("invalid endpoint response %q = %d/%q", body, w.Code, w.Header().Get("Content-Type"))
		}
	}
}

func TestGraphQLResponseShapeDoesNotCopyLargeData(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("race instrumentation changes allocation counts")
	}
	body := []byte(`{"data":{"value":"` + strings.Repeat("x", 1<<20) + `"}}`)
	allocations := testing.AllocsPerRun(100, func() {
		data, errors, valid := graphQLResponseShape(body)
		if !valid || !data || errors {
			t.Fatal("valid response shape rejected")
		}
	})
	if allocations > 3 {
		t.Fatalf("response validation allocated %.0f times", allocations)
	}
}

func TestGETAllowsQueryButRejectsMutation(t *testing.T) {
	ep := &testEndpoint{body: []byte(`{"data":{}}`)}
	h := NewHandler(ep, Options{})
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
	NewHandler(ep, Options{}).ServeHTTP(w, req(http.MethodPost, "application/json", "application/graphql-response+json", `{"query":"{x}"}`))
	if w.Code != http.StatusUnprocessableEntity || w.Body.String() != string(ep.body) {
		t.Fatalf("response = %d %s", w.Code, w.Body)
	}
}

func TestPartialExecutionUses200AndKeepsGraphQLBody(t *testing.T) {
	ep := &testEndpoint{body: []byte(`{"data":null,"errors":[{"message":"failed"}]}`)}
	w := httptest.NewRecorder()
	NewHandler(ep, Options{}).ServeHTTP(w, req(http.MethodPost, "application/json", "application/graphql-response+json", `{"query":"{x}"}`))
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
			NewHandler(ep, Options{}).ServeHTTP(w, req(http.MethodPost, "application/json", "application/graphql-response+json", test.body))
			if w.Code != test.status || ep.ctx != nil {
				t.Fatalf("response/called = %d/%v", w.Code, ep.ctx != nil)
			}
		})
	}
}

func TestGraphQLRequestErrorsAlwaysHaveCode(t *testing.T) {
	w := httptest.NewRecorder()
	NewHandler(&testEndpoint{}, Options{}).ServeHTTP(w, req(http.MethodPost, "application/json", "application/graphql-response+json", `{"query":"{"}`))
	var body struct {
		Errors []struct {
			Extensions map[string]any `json:"extensions"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Errors) != 1 || body.Errors[0].Extensions["code"] != "GRAPHQL_PARSE_FAILED" {
		t.Fatalf("errors = %#v", body.Errors)
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

func TestHTTPAdmissionBoundsDecodedAndExecutingRequests(t *testing.T) {
	ep := &blockingEndpoint{entered: make(chan struct{}, 2), release: make(chan struct{}, 2)}
	h := NewHandler(ep, Options{MaxConcurrent: 1, MaxQueued: 1, Timeout: time.Second})

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req(http.MethodPost, "application/json", responseType, `{"query":"{x}"}`))
		firstDone <- w
	}()
	<-ep.entered

	secondRead := make(chan struct{}, 1)
	secondDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := req(http.MethodPost, "application/json", responseType, "")
		r.Body = io.NopCloser(&signalingReader{reader: strings.NewReader(`{"query":"{x}"}`), read: secondRead})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		secondDone <- w
	}()
	<-secondRead

	overloaded := req(http.MethodPost, "application/json", responseType, "")
	overloaded.Body = io.NopCloser(panicReader{})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, overloaded)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("overload response = %d retry-after=%q body=%s", w.Code, w.Header().Get("Retry-After"), w.Body.Bytes())
	}
	requireTransportError(t, w, "REQUEST_OVERLOADED")
	if got := ep.calls.Load(); got != 1 {
		t.Fatalf("endpoint calls before release = %d, want 1", got)
	}

	ep.release <- struct{}{}
	if w := <-firstDone; w.Code != http.StatusOK {
		t.Fatalf("first status = %d", w.Code)
	}
	<-ep.entered
	ep.release <- struct{}{}
	if w := <-secondDone; w.Code != http.StatusOK {
		t.Fatalf("second status = %d", w.Code)
	}
}

func TestResponseBodyLimitRejectsBeforeWritingGraphQL(t *testing.T) {
	ep := &testEndpoint{body: []byte(`{"data":{"value":"` + strings.Repeat("x", 64) + `"}}`)}
	w := httptest.NewRecorder()
	NewHandler(ep, Options{MaxResponseBytes: 32}).ServeHTTP(w, req(http.MethodPost, "application/json", "application/graphql-response+json", `{"query":"{x}"}`))
	if w.Code != http.StatusInternalServerError || w.Header().Get("Content-Type") != responseType || bytes.Contains(w.Body.Bytes(), ep.body) || !bytes.Contains(w.Body.Bytes(), []byte(`"code":"LIMIT_EXCEEDED"`)) {
		t.Fatalf("oversized response = %d/%q %s", w.Code, w.Header().Get("Content-Type"), w.Body.Bytes())
	}
}
