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
	jsoniter "github.com/json-iterator/go"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// Endpoint reuses the core transport-neutral execution contract.
type Endpoint = core.Endpoint

type snapshotPinner interface {
	ForSnapshot(string) (core.ComposableEndpoint, error)
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
	executionPinHeader  = "GQL-Execution-Pin"
)

func delegationToken(r *http.Request) (string, bool) {
	return strings.CutPrefix(r.Header.Get("Authorization"), delegationScheme)
}

// NewHandler returns a GraphQL-over-HTTP handler.
func NewHandler(endpoint Endpoint, opts Options) http.Handler {
	opts = defaultOptions(opts)
	return &handler{endpoint: endpoint, options: opts}
}

func defaultOptions(opts Options) Options {
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = defaultMaxBodyBytes
	}
	if opts.MaxResponseBytes <= 0 {
		opts.MaxResponseBytes = defaultMaxResponse
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	return opts
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

type codedError struct{ message, code string }

func (e codedError) Error() string     { return e.message }
func (e codedError) ErrorCode() string { return e.code }

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.endpoint == nil {
		writeProblem(w, http.StatusInternalServerError, "ENDPOINT_UNAVAILABLE", "endpoint unavailable", true)
		return
	}
	responseMedia := negotiateGraphQL(r.Header.Get("Accept"))
	if responseMedia == "" {
		writeProblem(w, http.StatusNotAcceptable, "NOT_ACCEPTABLE", "unsupported Accept", false)
		return
	}
	var req request
	switch r.Method {
	case http.MethodPost:
		if !isJSONContentType(r.Header.Get("Content-Type")) {
			writeProblem(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Content-Type must be application/json", false)
			return
		}
		if err := decodeLimited(r, h.options.MaxBodyBytes, &req); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeProblem(w, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "request body too large", false)
				return
			}
			var typeErr *json.UnmarshalTypeError
			if errors.As(err, &typeErr) {
				writeProblem(w, http.StatusUnprocessableEntity, "INVALID_REQUEST", "invalid GraphQL request parameters", false)
				return
			}
			writeProblem(w, http.StatusBadRequest, "INVALID_JSON", err.Error(), false)
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
				writeProblem(w, http.StatusUnprocessableEntity, "INVALID_REQUEST", "invalid GraphQL request parameters", false)
			} else {
				writeProblem(w, http.StatusBadRequest, "INVALID_JSON", err.Error(), false)
			}
			return
		}
	default:
		w.Header().Set("Allow", "GET, POST")
		writeProblem(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", false)
		return
	}
	// A blank query is a valid persisted-operation request when the endpoint
	// supports it; policy enforcement remains in the endpoint.
	if req.Query == "" && req.OperationName == "" {
		writeProblem(w, http.StatusUnprocessableEntity, "INVALID_REQUEST", "query is required", false)
		return
	}
	ctx := r.Context()
	ctx = core.WithResponseLimit(ctx, h.options.MaxResponseBytes)
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
			writeProblem(w, http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "delegation required", false)
			return
		}
		var authErr error
		ctx, authErr = h.options.Delegation.Verify(ctx, token, operationDigest(req, r.Header.Get("GQL-Snapshot"), r.Header.Get(executionPinHeader)))
		if authErr != nil {
			writeProblem(w, http.StatusUnauthorized, "INVALID_DELEGATION", "invalid delegation", false)
			return
		}
		ctx = core.WithDelegatedExecution(ctx)
	}
	execution := h.endpoint
	revision := r.Header.Get("GQL-Snapshot")
	if revision != "" {
		if h.options.Delegation == nil {
			source, ok := h.endpoint.(manifestEndpoint)
			if !ok {
				writeProblem(w, http.StatusConflict, "SNAPSHOT_UNAVAILABLE", "snapshot pinning unavailable", true)
				return
			}
			current, snapshotErr := source.Snapshot()
			if snapshotErr != nil || current == nil || current.Fingerprint() != revision {
				writeProblem(w, http.StatusConflict, "SNAPSHOT_CHANGED", "snapshot revision changed", true)
				return
			}
		}
		if pinner, ok := h.endpoint.(snapshotPinner); ok {
			pinned, pinErr := pinner.ForSnapshot(revision)
			var executable bool
			execution, executable = pinned.(Endpoint)
			if pinErr != nil || !executable {
				writeProblem(w, http.StatusConflict, "SNAPSHOT_CHANGED", "snapshot revision changed", true)
				return
			}
		} else {
			source, ok := h.endpoint.(manifestEndpoint)
			if !ok {
				writeProblem(w, http.StatusConflict, "SNAPSHOT_UNAVAILABLE", "snapshot pinning unavailable", true)
				return
			}
			snapshot, snapshotErr := source.Snapshot()
			if snapshotErr != nil || snapshot == nil || snapshot.Fingerprint() != revision {
				writeProblem(w, http.StatusConflict, "SNAPSHOT_CHANGED", "snapshot revision changed", true)
				return
			}
		}
	}
	if revision == "" {
		if source, ok := h.endpoint.(manifestEndpoint); ok {
			if pinner, pinnable := h.endpoint.(snapshotPinner); pinnable {
				snapshot, snapshotErr := source.Snapshot()
				if snapshotErr != nil || snapshot == nil {
					writeProblem(w, http.StatusServiceUnavailable, "SNAPSHOT_UNAVAILABLE", "snapshot unavailable", true)
					return
				}
				pinned, pinErr := pinner.ForSnapshot(snapshot.Fingerprint())
				var executable bool
				execution, executable = pinned.(Endpoint)
				if pinErr != nil || !executable {
					writeProblem(w, http.StatusConflict, "SNAPSHOT_CHANGED", "snapshot revision changed", true)
					return
				}
			}
		}
	}
	if pin := r.Header.Get(executionPinHeader); pin != "" {
		source, ok := execution.(effectiveSchemaEndpoint)
		if !ok {
			writeProblem(w, http.StatusConflict, "SNAPSHOT_CONFLICT", "effective schema execution pin unavailable", true)
			return
		}
		artifact, err := source.EffectiveSchema(ctx)
		if err != nil || artifact.ExecutionPin == "" || artifact.ExecutionPin != pin {
			writeProblem(w, http.StatusConflict, "SNAPSHOT_CONFLICT", "effective schema execution pin changed", true)
			return
		}
		ctx = core.WithExecutionPin(ctx, pin)
	}
	if r.Method == http.MethodGet && req.Query == "" {
		w.Header().Set("Allow", "POST")
		writeProblem(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "GET requires an explicit query operation", false)
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
		writeProblem(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "GET only allows query operations", false)
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
			writeGraphQLError(w, http.StatusInternalServerError, codedError{"GraphQL response exceeds configured limit", "LIMIT_EXCEEDED"}, responseMedia)
			return
		}
		if writeGraphQL(w, body, responseMedia) {
			return
		}
		err = codedError{"endpoint returned an invalid GraphQL response", "BAD_GATEWAY_RESPONSE"}
	}
	if err != nil {
		writeGraphQLError(w, http.StatusInternalServerError, err, responseMedia)
		return
	}
	writeGraphQLError(w, http.StatusInternalServerError, codedError{"empty execution response", "BAD_GATEWAY_RESPONSE"}, responseMedia)
}

func operationDigest(req request, revision, executionPin string) string {
	body, _ := json.Marshal(struct {
		Request      request `json:"request"`
		Revision     string  `json:"revision,omitempty"`
		ExecutionPin string  `json:"executionPin,omitempty"`
	}{req, revision, executionPin})
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
	data, _, valid := graphQLResponseShape(body)
	if !valid {
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

func graphQLResponseShape(body []byte) (data, errors, valid bool) {
	iter := jsoniter.ConfigFastest.BorrowIterator(body)
	defer jsoniter.ConfigFastest.ReturnIterator(iter)
	dataValue := false
	iter.ReadObjectCB(func(iter *jsoniter.Iterator, field string) bool {
		switch field {
		case "data":
			data = true
			if iter.WhatIsNext() == jsoniter.NilValue {
				iter.ReadNil()
			} else {
				dataValue = true
				iter.Skip()
			}
		case "errors":
			count := 0
			iter.ReadArrayCB(func(iter *jsoniter.Iterator) bool { count++; iter.Skip(); return true })
			errors = count > 0
		default:
			iter.Skip()
		}
		return true
	})
	return data, errors, iter.Error == nil && (errors || data && dataValue)
}

func writeGraphQLRequestError(w http.ResponseWriter, status int, err error, mediaType string) {
	code := "GRAPHQL_REQUEST_INVALID"
	if status == http.StatusBadRequest {
		code = "GRAPHQL_PARSE_FAILED"
	}
	body, _ := json.Marshal(map[string]any{"errors": []map[string]any{{"message": err.Error(), "extensions": map[string]string{"code": code}}}})
	if mediaType == "application/json" {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", mediaType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeProblem(w http.ResponseWriter, status int, code, message string, retryable bool) {
	body, _ := json.Marshal(map[string]any{
		"version": 1,
		"error":   map[string]any{"code": code, "message": message, "retryable": retryable},
	})
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeGraphQLError(w http.ResponseWriter, status int, err error, mediaType string) {
	body, _ := json.Marshal(map[string]any{"errors": core.GraphQLErrors(err)})
	w.Header().Set("Content-Type", mediaType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
