// Package transport contains small, framework-independent GraphQL transports.
package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	core "github.com/ichaly/ideabase/gql"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// Endpoint is the transport-neutral execution contract implemented by gql.Executor and gql.Gateway.
type Endpoint interface {
	Execute(context.Context, string, map[string]any) ([]byte, error)
}

type operationEndpoint interface {
	ExecuteOperation(context.Context, string, map[string]any, string) ([]byte, error)
}

// Options controls the HTTP resource limits. Zero values use safe defaults.
type Options struct {
	MaxBodyBytes int64
	Timeout      time.Duration
	Delegation   DelegationVerifier
}

// DelegationVerifier authenticates a signed identity bound to one request.
type DelegationVerifier interface {
	Verify(context.Context, string, string) (context.Context, error)
}

const (
	defaultMaxBodyBytes = 1 << 20
	defaultTimeout      = 30 * time.Second
	responseType        = "application/graphql-response+json"
)

// NewHandler returns a GraphQL-over-HTTP handler.
func NewHandler(endpoint Endpoint, options ...Options) http.Handler {
	var opts Options
	if len(options) != 0 {
		opts = options[0]
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = defaultMaxBodyBytes
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	return &handler{endpoint: endpoint, options: opts}
}

// NewHTTPHandler is an explicit alias for NewHandler.
func NewHTTPHandler(endpoint Endpoint, options ...Options) http.Handler {
	return NewHandler(endpoint, options...)
}

type handler struct {
	endpoint Endpoint
	options  Options
}

type request struct {
	Query         string         `json:"query"`
	OperationName string         `json:"operationName,omitempty"`
	Variables     map[string]any `json:"variables,omitempty"`
	Extensions    map[string]any `json:"extensions,omitempty"`
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.endpoint == nil {
		http.Error(w, "endpoint unavailable", http.StatusInternalServerError)
		return
	}
	if !acceptsGraphQL(r.Header.Get("Accept")) {
		http.Error(w, "unsupported Accept", http.StatusNotAcceptable)
		return
	}
	var req request
	switch r.Method {
	case http.MethodPost:
		if !isJSONContentType(r.Header.Get("Content-Type")) {
			http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
		if err := decodeLimited(r, h.options.MaxBodyBytes, &req); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			var typeErr *json.UnmarshalTypeError
			if errors.As(err, &typeErr) {
				http.Error(w, "invalid GraphQL request parameters", http.StatusUnprocessableEntity)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	case http.MethodGet:
		var err error
		req.Query = r.URL.Query().Get("query")
		req.OperationName = r.URL.Query().Get("operationName")
		if raw := r.URL.Query().Get("variables"); raw != "" {
			err = decodeJSON([]byte(raw), &req.Variables)
		}
		if err == nil {
			if raw := r.URL.Query().Get("extensions"); raw != "" {
				err = decodeJSON([]byte(raw), &req.Extensions)
			}
		}
		if err != nil {
			var typeErr *json.UnmarshalTypeError
			if errors.As(err, &typeErr) {
				http.Error(w, "invalid GraphQL request parameters", http.StatusUnprocessableEntity)
			} else {
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
			return
		}
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if req.Query == "" {
		http.Error(w, "query is required", http.StatusUnprocessableEntity)
		return
	}
	ctx := r.Context()
	if h.options.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.options.Timeout)
		defer cancel()
	}
	if r.Header.Get("GQL-Mutation-Commit") == "1" {
		ctx = core.WithMutationCommitMetadata(ctx)
	}
	if h.options.Delegation != nil {
		const scheme = "GQL-Delegation "
		authorization := r.Header.Get("Authorization")
		if !strings.HasPrefix(authorization, scheme) {
			http.Error(w, "delegation required", http.StatusUnauthorized)
			return
		}
		var authErr error
		ctx, authErr = h.options.Delegation.Verify(ctx, strings.TrimPrefix(authorization, scheme), operationDigest(req))
		if authErr != nil {
			http.Error(w, "invalid delegation", http.StatusUnauthorized)
			return
		}
	}
	if revision := r.Header.Get("GQL-Snapshot"); revision != "" {
		source, ok := h.endpoint.(manifestEndpoint)
		if !ok {
			http.Error(w, "snapshot pinning unavailable", http.StatusConflict)
			return
		}
		snapshot, snapshotErr := source.Snapshot()
		if snapshotErr != nil || snapshot.Fingerprint() != revision {
			http.Error(w, "snapshot revision changed", http.StatusConflict)
			return
		}
	}
	kind, status, operationErr := requestOperation(req.Query, req.OperationName)
	if operationErr != nil {
		writeGraphQLRequestError(w, status, operationErr)
		return
	}
	if r.Method == http.MethodGet && kind != ast.Query {
		w.Header().Set("Allow", "POST")
		http.Error(w, "GET only allows query operations", http.StatusMethodNotAllowed)
		return
	}
	var body []byte
	var err error
	if named, ok := h.endpoint.(operationEndpoint); ok {
		body, err = named.ExecuteOperation(ctx, req.Query, req.Variables, req.OperationName)
	} else {
		body, err = h.endpoint.Execute(ctx, req.Query, req.Variables)
	}
	if len(body) != 0 {
		if writeGraphQL(w, body) {
			return
		}
		err = fmt.Errorf("endpoint returned an invalid GraphQL response")
	}
	if err != nil {
		writeGraphQLError(w, http.StatusInternalServerError, err)
		return
	}
	writeGraphQLError(w, http.StatusInternalServerError, fmt.Errorf("empty execution response"))
}

func operationDigest(req request) string {
	body, _ := json.Marshal(req)
	return fmt.Sprintf("%x", sha256.Sum256(body))
}

func decodeLimited(r *http.Request, limit int64, dst any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func decodeJSON(raw []byte, dst any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("JSON must contain one value")
		}
		return err
	}
	return nil
}

func acceptsGraphQL(value string) bool {
	for _, part := range strings.Split(value, ",") {
		mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil || mediaType != responseType {
			continue
		}
		quality := 1.0
		if raw := params["q"]; raw != "" {
			quality, err = strconv.ParseFloat(raw, 64)
			if err != nil {
				continue
			}
		}
		if quality > 0 {
			return true
		}
	}
	return false
}

func isJSONContentType(value string) bool {
	mediaType, params, err := mime.ParseMediaType(value)
	if err != nil || mediaType != "application/json" {
		return false
	}
	charset := strings.ToLower(params["charset"])
	return charset == "" || charset == "utf-8" || charset == "utf8"
}

func requestOperation(query, operationName string) (ast.Operation, int, error) {
	doc, err := parser.ParseQuery(&ast.Source{Input: query})
	if err != nil {
		return "", http.StatusBadRequest, err
	}
	op := doc.Operations.ForName(operationName)
	if op == nil && operationName == "" && len(doc.Operations) == 1 {
		op = doc.Operations[0]
	}
	if op == nil {
		return "", http.StatusUnprocessableEntity, fmt.Errorf("operation cannot be determined")
	}
	return op.Operation, 0, nil
}

func writeGraphQL(w http.ResponseWriter, body []byte) bool {
	var response map[string]json.RawMessage
	if err := decodeJSON(body, &response); err != nil {
		return false
	}
	_, data := response["data"]
	rawErrors, errors := response["errors"]
	if !data && !errors {
		return false
	}
	if errors {
		var list []json.RawMessage
		if err := json.Unmarshal(rawErrors, &list); err != nil || len(list) == 0 {
			return false
		}
	}
	if data && !errors && string(bytes.TrimSpace(response["data"])) == "null" {
		return false
	}
	status := http.StatusOK
	if data && errors {
		status = 294
	} else if !data {
		status = http.StatusUnprocessableEntity
	}
	w.Header().Set("Content-Type", responseType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
	return true
}

func writeGraphQLRequestError(w http.ResponseWriter, status int, err error) {
	body, _ := json.Marshal(map[string]any{"errors": []map[string]string{{"message": err.Error()}}})
	w.Header().Set("Content-Type", responseType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeGraphQLError(w http.ResponseWriter, status int, err error) {
	body, _ := json.Marshal(map[string]any{"errors": []map[string]string{{"message": err.Error()}}})
	// Failures that prevent a GraphQL response use ordinary JSON as required by
	// GraphQL-over-HTTP; execution errors already arrive as endpoint bodies.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
