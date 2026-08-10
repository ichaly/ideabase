package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	core "github.com/ichaly/ideabase/gql"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

const endpointProtocolVersion = 1

// Manifest is the immutable wire contract used to compose a remote endpoint.
type Manifest struct {
	Version      int               `json:"version"`
	EndpointID   string            `json:"endpointId"`
	Schema       string            `json:"schema"`
	Fingerprint  string            `json:"fingerprint"`
	Capabilities []core.Capability `json:"capabilities"`
	Subscription bool              `json:"subscription"`
	Health       bool              `json:"health"`
}

type manifestEndpoint interface {
	Snapshot() (*core.Snapshot, error)
}

// NewManifestHandler exposes one frozen runtime manifest for remote composition.
func NewManifestHandler(id string, endpoint manifestEndpoint) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if id == "" || endpoint == nil {
			http.Error(w, "manifest unavailable", http.StatusServiceUnavailable)
			return
		}
		snapshot, err := endpoint.Snapshot()
		if err != nil {
			http.Error(w, "manifest unavailable", http.StatusServiceUnavailable)
			return
		}
		manifest := Manifest{
			Version: endpointProtocolVersion, EndpointID: id, Schema: snapshot.Schema(), Fingerprint: snapshot.Fingerprint(),
			Capabilities: snapshot.Capabilities(),
		}
		if source, ok := endpoint.(interface{ SubscriptionAvailable() bool }); ok {
			manifest.Subscription = source.SubscriptionAvailable()
		}
		if source, ok := endpoint.(interface{ HealthAvailable() bool }); ok {
			manifest.Health = source.HealthAvailable()
		} else if _, ok := endpoint.(interface {
			Health(context.Context) (core.HealthReport, error)
		}); ok {
			manifest.Health = true
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"`+snapshot.Fingerprint()+`"`)
		_ = json.NewEncoder(w).Encode(manifest)
	})
}

// RemoteOptions fixes every network boundary at startup; redirects are always denied.
type RemoteOptions struct {
	EndpointID, ManifestURL, ExecuteURL, SubscribeURL, HealthURL string
	Client                                                       *http.Client
	Delegation                                                   interface {
		Sign(context.Context, string) (string, error)
	}
	Timeout          time.Duration
	MaxResponseBytes int64
	AllowHTTP        bool
}

// RemoteEndpoint implements gql.Endpoint over GraphQL-over-HTTP.
type RemoteEndpoint struct {
	id       string
	manifest string
	execute  string
	client   *http.Client
	signer   interface {
		Sign(context.Context, string) (string, error)
	}
	maxResponse int64
	timeout     time.Duration
	subscribe   string
	health      string
	state       atomic.Pointer[remoteState]
}

// remoteState is published as one pointer so snapshot, subscription and
// health capability bits always describe the same manifest revision.
type remoteState struct {
	snapshot              *core.Snapshot
	subscriptionAvailable bool
	healthAvailable       bool
}

type remoteRevision struct {
	endpoint              *RemoteEndpoint
	snapshot              *core.Snapshot
	subscriptionAvailable bool
	healthAvailable       bool
}

func (my *remoteRevision) EndpointID() string                { return my.endpoint.id }
func (my *remoteRevision) Snapshot() (*core.Snapshot, error) { return my.snapshot, nil }
func (my *remoteRevision) Execute(ctx context.Context, query string, variables map[string]any) ([]byte, error) {
	return my.endpoint.executeOperation(ctx, query, variables, "", my.snapshot.Fingerprint())
}
func (my *remoteRevision) ExecuteOperation(ctx context.Context, query string, variables map[string]any, operationName string) ([]byte, error) {
	return my.endpoint.executeOperation(ctx, query, variables, operationName, my.snapshot.Fingerprint())
}
func (my *remoteRevision) Subscribe(ctx context.Context, query string, variables map[string]any, operationName string) (<-chan []byte, error) {
	if !my.subscriptionAvailable {
		return nil, fmt.Errorf("remote subscription unavailable for snapshot")
	}
	return my.endpoint.subscribeOperation(ctx, query, variables, operationName, my.snapshot.Fingerprint())
}
func (my *remoteRevision) SubscriptionAvailable() bool { return my.subscriptionAvailable }
func (my *remoteRevision) HealthAvailable() bool       { return my.healthAvailable }
func (my *remoteRevision) Health(ctx context.Context) (core.HealthReport, error) {
	report, err := my.endpoint.Health(ctx)
	if report.SnapshotFingerprint != my.snapshot.Fingerprint() {
		report.Ready, report.SnapshotOK = false, false
		if err == nil {
			err = fmt.Errorf("remote health snapshot mismatch")
		}
	}
	return report, err
}

func NewRemoteEndpoint(ctx context.Context, options RemoteOptions) (*RemoteEndpoint, error) {
	if options.EndpointID == "" || options.ManifestURL == "" || options.ExecuteURL == "" {
		return nil, fmt.Errorf("remote endpoint id, manifest URL and execute URL are required")
	}
	urls := []string{options.ManifestURL, options.ExecuteURL}
	if options.SubscribeURL != "" {
		urls = append(urls, options.SubscribeURL)
	}
	if options.HealthURL != "" {
		urls = append(urls, options.HealthURL)
	}
	for _, raw := range urls {
		if !remoteURLAllowed(raw, options.AllowHTTP) {
			return nil, fmt.Errorf("remote endpoint URL is not allowed: %s", raw)
		}
	}
	if options.Timeout <= 0 {
		options.Timeout = 10 * time.Second
	}
	if options.MaxResponseBytes <= 0 {
		options.MaxResponseBytes = 8 << 20
	}
	client := options.Client
	if client == nil {
		client = &http.Client{}
	} else {
		copy := *client
		client = &copy
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	remote := &RemoteEndpoint{
		id: options.EndpointID, manifest: options.ManifestURL, execute: options.ExecuteURL,
		client: client, signer: options.Delegation, maxResponse: options.MaxResponseBytes, timeout: options.Timeout, subscribe: options.SubscribeURL,
		health: options.HealthURL,
	}
	if err := remote.Refresh(ctx); err != nil {
		return nil, err
	}
	return remote, nil
}

func remoteURLAllowed(raw string, allowHTTP bool) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	host := parsed.Hostname()
	return allowHTTP && parsed.Scheme == "http" && (strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback())
}

func (my *RemoteEndpoint) EndpointID() string { return my.id }

func (my *RemoteEndpoint) SubscriptionAvailable() bool {
	state := my.state.Load()
	return state != nil && state.subscriptionAvailable
}
func (my *RemoteEndpoint) HealthAvailable() bool {
	state := my.state.Load()
	return state != nil && state.healthAvailable
}

func (my *RemoteEndpoint) Snapshot() (*core.Snapshot, error) {
	state := my.state.Load()
	if state == nil || state.snapshot == nil {
		return nil, fmt.Errorf("remote snapshot unavailable")
	}
	return state.snapshot, nil
}

func (my *RemoteEndpoint) ForSnapshot(fingerprint string) (core.Endpoint, error) {
	state := my.state.Load()
	if state == nil || state.snapshot == nil || state.snapshot.Fingerprint() != fingerprint {
		return nil, fmt.Errorf("remote snapshot revision unavailable")
	}
	return &remoteRevision{endpoint: my, snapshot: state.snapshot, subscriptionAvailable: state.subscriptionAvailable, healthAvailable: state.healthAvailable}, nil
}

// Refresh atomically installs a fully validated manifest or retains the LKG snapshot.
func (my *RemoteEndpoint) Refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, my.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, my.manifest, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := my.client.Do(request)
	if err != nil {
		return fmt.Errorf("remote manifest unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("remote manifest status %d", response.StatusCode)
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		return fmt.Errorf("remote manifest content type %q", mediaType)
	}
	var manifest Manifest
	if err := decodeResponse(response.Body, my.maxResponse, &manifest); err != nil {
		return fmt.Errorf("remote manifest: %w", err)
	}
	if manifest.Version != endpointProtocolVersion || manifest.EndpointID != my.id {
		return fmt.Errorf("remote manifest identity or protocol mismatch")
	}
	parsed, err := gqlparser.LoadSchema(&ast.Source{Name: "remote.graphql", Input: manifest.Schema})
	if err != nil || (parsed.Subscription != nil) != manifest.Subscription || (manifest.Subscription && my.subscribe == "") {
		return fmt.Errorf("remote subscription manifest is inconsistent")
	}
	if manifest.Health != (my.health != "") {
		return fmt.Errorf("remote health manifest is inconsistent")
	}
	snapshot, err := core.NewSnapshotArtifact(manifest.Schema, manifest.Fingerprint, manifest.Capabilities)
	if err != nil {
		return err
	}
	my.state.Store(&remoteState{snapshot: snapshot, subscriptionAvailable: manifest.Subscription, healthAvailable: manifest.Health})
	return nil
}

func (my *RemoteEndpoint) Execute(ctx context.Context, query string, variables map[string]any) ([]byte, error) {
	return my.ExecuteOperation(ctx, query, variables, "")
}

func (my *RemoteEndpoint) ExecuteOperation(ctx context.Context, query string, variables map[string]any, operationName string) ([]byte, error) {
	return my.executeOperation(ctx, query, variables, operationName, "")
}

func (my *RemoteEndpoint) executeOperation(ctx context.Context, query string, variables map[string]any, operationName, fingerprint string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, my.timeout)
	defer cancel()
	payload := request{Query: query, OperationName: operationName, Variables: variables}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, my.execute, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", responseType)
	if core.MutationCommitMetadataRequested(ctx) {
		request.Header.Set("GQL-Mutation-Commit", "1")
	}
	if fingerprint != "" {
		request.Header.Set("GQL-Snapshot", fingerprint)
	}
	if my.signer != nil {
		token, signErr := my.signer.Sign(ctx, operationDigest(payload))
		if signErr != nil {
			return nil, signErr
		}
		request.Header.Set("Authorization", "GQL-Delegation "+token)
	}
	response, err := my.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("remote execute unavailable: %w", err)
	}
	defer response.Body.Close()
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType != responseType {
		return nil, fmt.Errorf("remote execute status %d content type %q", response.StatusCode, mediaType)
	}
	return readLimited(response.Body, my.maxResponse)
}

// Subscribe consumes the GraphQL SSE single-operation protocol.
func (my *RemoteEndpoint) Subscribe(ctx context.Context, query string, variables map[string]any, operationName string) (<-chan []byte, error) {
	return my.subscribeOperation(ctx, query, variables, operationName, "")
}

func (my *RemoteEndpoint) subscribeOperation(ctx context.Context, query string, variables map[string]any, operationName, fingerprint string) (<-chan []byte, error) {
	if my.subscribe == "" {
		return nil, fmt.Errorf("remote subscription unavailable")
	}
	payload := request{Query: query, OperationName: operationName, Variables: variables}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, my.subscribe, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	if fingerprint != "" {
		request.Header.Set("GQL-Snapshot", fingerprint)
	}
	if my.signer != nil {
		token, signErr := my.signer.Sign(ctx, operationDigest(payload))
		if signErr != nil {
			return nil, signErr
		}
		request.Header.Set("Authorization", "GQL-Delegation "+token)
	}
	streamCtx, cancelStream := context.WithCancel(ctx)
	request = request.WithContext(streamCtx)
	result := make(chan struct {
		response *http.Response
		err      error
	}, 1)
	go func() {
		response, err := my.client.Do(request)
		if streamCtx.Err() != nil && response != nil {
			response.Body.Close()
			response = nil
		}
		result <- struct {
			response *http.Response
			err      error
		}{response, err}
	}()
	timer := time.NewTimer(my.timeout)
	defer timer.Stop()
	var resultValue struct {
		response *http.Response
		err      error
	}
	select {
	case resultValue = <-result:
	case <-timer.C:
		cancelStream()
		return nil, fmt.Errorf("remote subscription handshake timeout")
	}
	response, err := resultValue.response, resultValue.err
	if err != nil {
		cancelStream()
		return nil, fmt.Errorf("remote subscription unavailable: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		cancelStream()
		response.Body.Close()
		return nil, fmt.Errorf("remote subscription status %d", response.StatusCode)
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType != "text/event-stream" {
		cancelStream()
		response.Body.Close()
		return nil, fmt.Errorf("remote subscription content type %q", mediaType)
	}
	events := make(chan []byte, 1)
	go func() {
		defer cancelStream()
		my.readSubscription(ctx, response.Body, events)
	}()
	return events, nil
}

func (my *RemoteEndpoint) readSubscription(ctx context.Context, body io.ReadCloser, events chan<- []byte) {
	defer body.Close()
	defer close(events)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), int(my.maxResponse))
	event, data := "", make([]byte, 0, 1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if int64(len(data)+len(payload)+1) > my.maxResponse {
				sendStreamError(ctx, events, "remote subscription response limit exceeded")
				return
			}
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, payload...)
		case line == "":
			if event == "complete" {
				return
			}
			if event == "next" && len(data) > 0 {
				frame := append([]byte(nil), data...)
				select {
				case events <- frame:
				case <-ctx.Done():
					return
				}
			}
			event, data = "", data[:0]
		}
	}
	if scanner.Err() != nil && ctx.Err() == nil {
		sendStreamError(ctx, events, "remote subscription stream failed")
	}
}

func sendStreamError(ctx context.Context, events chan<- []byte, message string) {
	frame, _ := json.Marshal(map[string]any{"errors": []map[string]any{{"message": message, "extensions": map[string]any{"code": "BAD_GATEWAY_RESPONSE"}}}})
	select {
	case events <- frame:
	case <-ctx.Done():
	}
}

func decodeResponse(reader io.Reader, limit int64, value any) error {
	body, err := readLimited(reader, limit)
	if err != nil {
		return err
	}
	return decodeJSON(body, value)
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return body, nil
}
