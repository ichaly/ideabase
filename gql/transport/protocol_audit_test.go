package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestGraphQLHTTPProtocolAudit is intentionally table/loop driven. The case
// count is a guard against silently shrinking this compatibility surface.
func TestGraphQLHTTPProtocolAudit(t *testing.T) {
	type auditCase struct {
		name, method, accept, contentType, url, target, body, code string
		status, calls                                              int
		options                                                    Options
		headers                                                    map[string]string
	}
	valid := `{"query":"{x}"}`
	graphql := `{"data":{"ok":true}}`
	cases := make([]auditCase, 0, 80)
	add := func(prefix string, values []string, fn func(string) auditCase) {
		for _, value := range values {
			c := fn(value)
			c.name = prefix + "-" + strings.NewReplacer("/", "-", ";", "-", "=", "-").Replace(value)
			cases = append(cases, c)
		}
	}
	// Accept negotiation: q-values, wildcards, and unsupported media types.
	add("accept-supported", []string{"application/graphql-response+json", "application/json", "*/*", "application/*", "application/json;q=0.9, application/graphql-response+json;q=1", "application/graphql-response+json;q=0.4, application/json;q=0.8", "application/json; charset=utf-8", ""}, func(v string) auditCase {
		return auditCase{accept: v, contentType: "application/json", method: http.MethodPost, body: valid, target: graphql, status: http.StatusOK, calls: 1}
	})
	add("accept-rejected", []string{"text/plain", "image/*", "application/xml", "application/graphql-response+json;q=0", "application/json;q=0, text/plain;q=1", "application/json;q=bogus", "text/plain, image/png", "audio/*;q=0.2"}, func(v string) auditCase {
		return auditCase{accept: v, contentType: "application/json", method: http.MethodPost, body: valid, status: http.StatusNotAcceptable, code: "NOT_ACCEPTABLE"}
	})
	// Content-Type and charset handling.
	add("content-rejected", []string{"text/plain", "application/json;charset=iso-8859-1", "application/json; charset=utf-16", "application/graphql+json", "", "application/x-www-form-urlencoded"}, func(v string) auditCase {
		return auditCase{accept: responseType, contentType: v, method: http.MethodPost, body: valid, status: http.StatusUnsupportedMediaType, code: "UNSUPPORTED_MEDIA_TYPE"}
	})
	add("content-accepted", []string{"application/json", "application/json;charset=utf-8", "application/json; charset=UTF8", "application/json; charset=UTF-8", "application/json;foo=bar"}, func(v string) auditCase {
		return auditCase{accept: responseType, contentType: v, method: http.MethodPost, body: valid, target: graphql, status: http.StatusOK, calls: 1}
	})
	// POST JSON shape/type/trailing-value rejection.
	for i, body := range []string{``, `{`, `null`, `[]`, `{"query":1}`, `{"query":"{x}","variables":[]}`, `{"query":"{x}","extensions":[]}`, `{"query":"{x}"} {}`, `{"query":"{x}"} true`, `{"query":"{x}","operationName":1}`, `{}`} {
		status, code := http.StatusBadRequest, "INVALID_JSON"
		if i == 2 || i == 3 || i == 4 || i == 5 || i == 6 || i == 9 || i == 10 {
			status, code = http.StatusUnprocessableEntity, "INVALID_REQUEST"
		}
		cases = append(cases, auditCase{name: "post-shape-" + string(rune('a'+i)), method: http.MethodPost, accept: responseType, contentType: "application/json", body: body, status: status, code: code})
	}
	// GET query/variables/extensions encoding and malformed JSON.
	for i, target := range []string{"/graphql?query=%7Bx%7D", "/graphql?query=%7Bx%7D&variables=%7B%22id%22%3A1%7D", "/graphql?query=%7Bx%7D&extensions=%7B%22persisted%22%3Atrue%7D", "/graphql?query=%7Bx%7D&variables=%7B%7D&extensions=%7B%7D"} {
		cases = append(cases, auditCase{name: "get-valid-" + string(rune('a'+i)), method: http.MethodGet, accept: responseType, url: target, target: graphql, status: http.StatusOK, calls: 1})
	}
	for i, target := range []string{"/graphql?query=%7Bx%7D&variables=%5B%5D", "/graphql?query=%7Bx%7D&variables=%7B", "/graphql?query=%7Bx%7D&extensions=%5B%5D", "/graphql?query=%7Bx%7D&extensions=%7B"} {
		status, code := http.StatusUnprocessableEntity, "INVALID_REQUEST"
		if i%2 == 1 {
			status, code = http.StatusBadRequest, "INVALID_JSON"
		}
		cases = append(cases, auditCase{name: "get-invalid-" + string(rune('a'+i)), method: http.MethodGet, accept: responseType, url: target, status: status, code: code})
	}
	// Method safety, parse failure and operation ambiguity.
	for i, query := range []string{"mutation%20%7Bx%7D", "subscription%20%7Bx%7D", "mutation%7Bx%7D", "subscription%20%7Bx%7D"} {
		cases = append(cases, auditCase{name: "get-nonquery-" + string(rune('a'+i)), method: http.MethodGet, accept: responseType, url: "/graphql?query=" + query, status: http.StatusMethodNotAllowed, code: "METHOD_NOT_ALLOWED"})
	}
	for i, body := range []string{`{"query":"{"}`, `{"query":"query A{ x } query B{ x }"}`, `{"query":"query A{ x } query B{ x }","operationName":"Missing"}`, `{"query":"query A{ x } query B{ x }","operationName":"A"}`} {
		status, code := http.StatusBadRequest, "GRAPHQL_PARSE_FAILED"
		if i > 0 && i < 3 {
			status, code = http.StatusUnprocessableEntity, "GRAPHQL_REQUEST_INVALID"
		}
		calls := 0
		if i == 3 {
			status, code, calls = http.StatusOK, "", 1
		}
		cases = append(cases, auditCase{name: "operation-" + string(rune('a'+i)), method: http.MethodPost, accept: responseType, contentType: "application/json", body: body, target: graphql, status: status, code: code, calls: calls})
	}
	// Endpoint response media/status and operational guards.
	for i, target := range []string{`{"data":{"ok":true}}`, `{"data":null,"errors":[{"message":"partial"}]}`, `{"errors":[{"message":"request"}]}`, `not-json`} {
		status, calls := http.StatusOK, 1
		if i == 2 {
			status = http.StatusUnprocessableEntity
		}
		if i == 3 {
			status = http.StatusInternalServerError
		}
		cases = append(cases, auditCase{name: "response-" + string(rune('a'+i)), method: http.MethodPost, accept: responseType, contentType: "application/json", body: valid, target: target, status: status, calls: calls})
	}
	for i := 0; i < 6; i++ {
		cases = append(cases, auditCase{name: "method-" + string(rune('a'+i)), method: []string{http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions, "TRACE"}[i], accept: responseType, contentType: "application/json", body: valid, status: http.StatusMethodNotAllowed, code: "METHOD_NOT_ALLOWED"})
	}
	cases = append(cases,
		auditCase{name: "request-size", method: http.MethodPost, accept: responseType, contentType: "application/json", body: valid, options: Options{MaxBodyBytes: 4}, status: http.StatusRequestEntityTooLarge, code: "REQUEST_TOO_LARGE"},
		auditCase{name: "response-size", method: http.MethodPost, accept: responseType, contentType: "application/json", body: valid, target: strings.Repeat("x", 32), options: Options{MaxResponseBytes: 8}, status: http.StatusInternalServerError, calls: 1},
		auditCase{name: "execution-timeout", method: http.MethodPost, accept: responseType, contentType: "application/json", body: valid, target: graphql, options: Options{Timeout: time.Millisecond}, status: http.StatusInternalServerError, calls: 1},
		auditCase{name: "snapshot-pinning", method: http.MethodPost, accept: responseType, contentType: "application/json", body: valid, headers: map[string]string{"GQL-Snapshot": "old"}, status: http.StatusConflict, code: "SNAPSHOT_UNAVAILABLE"},
		auditCase{name: "authentication-required", method: http.MethodPost, accept: responseType, contentType: "application/json", body: valid, options: Options{Delegation: auditRejectVerifier{}}, status: http.StatusUnauthorized, code: "AUTHENTICATION_REQUIRED"},
	)
	if len(cases) < 61 {
		t.Fatalf("protocol audit generated only %d cases", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			ep := &auditEndpoint{body: []byte(tc.target), calls: &calls}
			if tc.name == "execution-timeout" {
				ep.delay = 20 * time.Millisecond
			}
			if tc.target == "" {
				ep.body = []byte(graphql)
			}
			h := NewHandler(ep, tc.options)
			var r *http.Request
			if tc.method == http.MethodGet {
				r = httptest.NewRequest(tc.method, tc.url, nil)
			} else {
				r = httptest.NewRequest(tc.method, "/graphql", strings.NewReader(tc.body))
			}
			r.Header.Set("Accept", tc.accept)
			if tc.contentType != "" {
				r.Header.Set("Content-Type", tc.contentType)
			}
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tc.status, w.Body.String())
			}
			if int(calls.Load()) != tc.calls {
				t.Fatalf("endpoint calls = %d, want %d", calls.Load(), tc.calls)
			}
			if tc.code != "" {
				requireAuditError(t, w, tc.code)
			}
		})
	}
}

func requireAuditError(t *testing.T, w *httptest.ResponseRecorder, want string) {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Errors []struct {
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	got := body.Error.Code
	if got == "" && len(body.Errors) > 0 {
		got = body.Errors[0].Extensions.Code
	}
	if got != want {
		t.Fatalf("error code = %q, want %q (%s)", got, want, w.Body.String())
	}
}

type auditEndpoint struct {
	body  []byte
	calls *atomic.Int32
	delay time.Duration
}

func (e *auditEndpoint) ExecuteOperation(ctx context.Context, _ string, _ map[string]any, _ string) ([]byte, error) {
	e.calls.Add(1)
	if e.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(e.delay):
		}
	}
	return e.body, nil
}

type auditRejectVerifier struct{}

func (auditRejectVerifier) Verify(context.Context, string, string) (context.Context, error) {
	return nil, errors.New("invalid token")
}
