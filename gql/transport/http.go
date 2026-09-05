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
	MaxConcurrent, MaxQueued       int
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
	defaultConcurrent   = 64
	defaultQueued       = 128
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
	return &handler{
		endpoint: endpoint, options: opts,
		admission:       make(chan struct{}, opts.MaxConcurrent+opts.MaxQueued),
		execution:       make(chan struct{}, opts.MaxConcurrent),
		streamAdmission: make(chan struct{}, opts.MaxConcurrent+opts.MaxQueued),
		streams:         make(chan struct{}, opts.MaxConcurrent),
	}
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
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = defaultConcurrent
	}
	if opts.MaxQueued <= 0 {
		opts.MaxQueued = defaultQueued
	}
	return opts
}

type handler struct {
	endpoint                 Endpoint
	options                  Options
	admission, execution     chan struct{}
	streamAdmission, streams chan struct{}
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
	streaming := responseMedia == "text/event-stream"
	admission, slots := h.admission, h.execution
	if streaming {
		admission, slots = h.streamAdmission, h.streams
	}
	select {
	case admission <- struct{}{}:
		defer func() { <-admission }()
	default:
		w.Header().Set("Retry-After", "1")
		writeProblem(w, http.StatusTooManyRequests, "REQUEST_OVERLOADED", "request capacity exhausted", true)
		return
	}
	if streaming && r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeProblem(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "SSE requires POST", false)
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
	if !streaming && h.options.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.options.Timeout)
		defer cancel()
	}
	if r.Header.Get("GQL-Mutation-Commit") == "1" {
		ctx = core.WithMutationCommitMetadata(ctx)
	}
	execution := h.endpoint
	ctx, execution, revision, failure := prepareRequest(ctx, r, h.endpoint, h.options.Delegation,
		operationDigest(req, r.Header.Get("GQL-Snapshot"), r.Header.Get(executionPinHeader)))
	if failure != nil {
		writeRequestFailure(w, failure)
		return
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
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		writeProblem(w, http.StatusServiceUnavailable, "REQUEST_TIMEOUT", "request expired while awaiting execution", true)
		return
	}
	if streaming {
		source, ok := execution.(subscriptionEndpoint)
		if !ok {
			writeProblem(w, http.StatusServiceUnavailable, "SUBSCRIPTION_UNAVAILABLE", "subscription unavailable", true)
			return
		}
		h.subscribe(w, r, ctx, source, req, revision)
		return
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
		case responseType, "text/event-stream":
			candidate = mediaType
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
