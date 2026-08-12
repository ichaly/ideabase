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

// Endpoint is the transport-neutral execution contract implemented by gql.Engine and gql.Gateway.
type Endpoint interface {
	ExecuteOperation(context.Context, string, map[string]any, string) ([]byte, error)
}

type snapshotPinner interface {
	ForSnapshot(string) (core.Endpoint, error)
}

// Options controls the HTTP resource limits. Zero values use safe defaults.
type Options struct {
	MaxBodyBytes, MaxResponseBytes int64
	Timeout                        time.Duration
	Delegation                     DelegationVerifier
}

// DelegationVerifier authenticates a signed identity bound to one request.
type DelegationVerifier interface {
	Verify(context.Context, string, string) (context.Context, error)
}

const (
	defaultMaxBodyBytes = 1 << 20
	defaultMaxResponse  = 8 << 20
	defaultTimeout      = 30 * time.Second
	responseType        = "application/graphql-response+json"
	delegationScheme    = "GQL-Delegation "
)

func delegationToken(r *http.Request) (string, bool) {
	return strings.CutPrefix(r.Header.Get("Authorization"), delegationScheme)
}

// NewHandler returns a GraphQL-over-HTTP handler.
func NewHandler(endpoint Endpoint, options ...Options) http.Handler {
	var opts Options
	if len(options) != 0 {
		opts = options[0]
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = defaultMaxBodyBytes
	}
	if opts.MaxResponseBytes <= 0 {
		opts.MaxResponseBytes = defaultMaxResponse
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	return &handler{endpoint: endpoint, options: opts}
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
	responseMedia := negotiateGraphQL(r.Header.Get("Accept"))
	if responseMedia == "" {
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
	// A blank query is a valid persisted-operation request when the endpoint
	// supports it; policy enforcement remains in the endpoint.
	if req.Query == "" && req.OperationName == "" {
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
		token, ok := delegationToken(r)
		if !ok {
			http.Error(w, "delegation required", http.StatusUnauthorized)
			return
		}
		var authErr error
		ctx, authErr = h.options.Delegation.Verify(ctx, token, operationDigest(req, r.Header.Get("GQL-Snapshot")))
		if authErr != nil {
			http.Error(w, "invalid delegation", http.StatusUnauthorized)
			return
		}
	}
	execution := h.endpoint
	revision := r.Header.Get("GQL-Snapshot")
	if revision != "" {
		if h.options.Delegation == nil {
			source, ok := h.endpoint.(manifestEndpoint)
			if !ok {
				http.Error(w, "snapshot pinning unavailable", http.StatusConflict)
				return
			}
			current, snapshotErr := source.Snapshot()
			if snapshotErr != nil || current == nil || current.Fingerprint() != revision {
				http.Error(w, "snapshot revision changed", http.StatusConflict)
				return
			}
		}
		if pinner, ok := h.endpoint.(snapshotPinner); ok {
			pinned, pinErr := pinner.ForSnapshot(revision)
			var executable bool
			execution, executable = pinned.(Endpoint)
			if pinErr != nil || !executable {
				http.Error(w, "snapshot revision changed", http.StatusConflict)
				return
			}
		} else {
			source, ok := h.endpoint.(manifestEndpoint)
			if !ok {
				http.Error(w, "snapshot pinning unavailable", http.StatusConflict)
				return
			}
			snapshot, snapshotErr := source.Snapshot()
			if snapshotErr != nil || snapshot == nil || snapshot.Fingerprint() != revision {
				http.Error(w, "snapshot revision changed", http.StatusConflict)
				return
			}
		}
	}
	if revision == "" {
		if source, ok := h.endpoint.(manifestEndpoint); ok {
			if pinner, pinnable := h.endpoint.(snapshotPinner); pinnable {
				snapshot, snapshotErr := source.Snapshot()
				if snapshotErr != nil || snapshot == nil {
					http.Error(w, "snapshot unavailable", http.StatusServiceUnavailable)
					return
				}
				pinned, pinErr := pinner.ForSnapshot(snapshot.Fingerprint())
				var executable bool
				execution, executable = pinned.(Endpoint)
				if pinErr != nil || !executable {
					http.Error(w, "snapshot revision changed", http.StatusConflict)
					return
				}
			}
		}
	}
	if r.Method == http.MethodGet && req.Query == "" {
		w.Header().Set("Allow", "POST")
		http.Error(w, "GET requires an explicit query operation", http.StatusMethodNotAllowed)
		return
	}
	kind := ast.Query
	if req.Query != "" {
		var status int
		var operationErr error
		kind, status, operationErr = requestOperation(req.Query, req.OperationName)
		if operationErr != nil {
			writeGraphQLRequestError(w, status, operationErr, responseMedia)
			return
		}
	}
	if r.Method == http.MethodGet && kind != ast.Query {
		w.Header().Set("Allow", "POST")
		http.Error(w, "GET only allows query operations", http.StatusMethodNotAllowed)
		return
	}
	var body []byte
	var err error
	if source, ok := execution.(manifestEndpoint); ok {
		if snapshot, snapshotErr := source.Snapshot(); snapshotErr == nil && snapshot != nil {
			w.Header().Set("GQL-Snapshot", snapshot.Fingerprint())
		}
	}
	body, err = execution.ExecuteOperation(ctx, req.Query, req.Variables, req.OperationName)
	if len(body) != 0 {
		if int64(len(body)) > h.options.MaxResponseBytes {
			writeGraphQLError(w, http.StatusInternalServerError, fmt.Errorf("GraphQL response exceeds configured limit"))
			return
		}
		if writeGraphQL(w, body, responseMedia) {
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

func operationDigest(req request, revision string) string {
	body, _ := json.Marshal(struct {
		Request  request `json:"request"`
		Revision string  `json:"revision,omitempty"`
	}{Request: req, Revision: revision})
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
	dec := json.NewDecoder(bytes.NewReader(raw))
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

func negotiateGraphQL(value string) string {
	if strings.TrimSpace(value) == "" {
		return "application/json"
	}
	selected, best := "", -1.0
	for _, part := range strings.Split(value, ",") {
		mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		quality := 1.0
		if raw := params["q"]; raw != "" {
			quality, err = strconv.ParseFloat(raw, 64)
			if err != nil {
				continue
			}
		}
		candidate := ""
		switch mediaType {
		case responseType:
			candidate = responseType
		case "application/json", "application/*", "*/*":
			candidate = "application/json"
		}
		if candidate != "" && quality > 0 && quality > best {
			selected, best = candidate, quality
		}
	}
	return selected
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

func writeGraphQL(w http.ResponseWriter, body []byte, mediaType string) bool {
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
	if !data {
		status = http.StatusUnprocessableEntity
	}
	if mediaType == "application/json" {
		status = http.StatusOK // Legacy JSON cannot distinguish GraphQL request and field errors via HTTP.
	}
	w.Header().Set("Content-Type", mediaType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
	return true
}

func writeGraphQLRequestError(w http.ResponseWriter, status int, err error, mediaType string) {
	body, _ := json.Marshal(map[string]any{"errors": []map[string]string{{"message": err.Error()}}})
	if mediaType == "application/json" {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", mediaType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeGraphQLError(w http.ResponseWriter, status int, err error) {
	body, _ := json.Marshal(map[string]any{"errors": core.GraphQLErrors(err)})
	// Failures that prevent a GraphQL response use ordinary JSON as required by
	// GraphQL-over-HTTP; execution errors already arrive as endpoint bodies.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
