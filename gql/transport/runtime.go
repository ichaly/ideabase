package transport

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
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
	Revision     uint64            `json:"revision"`
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

type manifestStateEndpoint interface {
	ManifestState() (*core.Snapshot, uint64, error)
}

type effectiveSchemaEndpoint interface {
	EffectiveSchema(context.Context) (core.SchemaArtifact, error)
}

// ManifestAuthenticator is the explicit trust boundary for schema metadata.
type ManifestAuthenticator func(*http.Request) bool

func schemaDigest(schema string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(schema)))
}

// NewEffectiveSchemaHandler exposes identity-aware schema materialization over
// one authenticated control-plane path.
func NewEffectiveSchemaHandler(endpoint effectiveSchemaEndpoint, verifier DelegationVerifier) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if endpoint == nil || verifier == nil {
			http.Error(w, "effective schema authentication required", http.StatusUnauthorized)
			return
		}
		token, ok := delegationToken(r)
		if !ok {
			http.Error(w, "delegation required", http.StatusUnauthorized)
			return
		}
		ctx, err := verifier.Verify(r.Context(), token, operationDigest(request{}, "effective-schema"))
		if err != nil {
			http.Error(w, "invalid delegation", http.StatusUnauthorized)
			return
		}
		artifact, err := endpoint.EffectiveSchema(ctx)
		if err != nil {
			http.Error(w, "effective schema unavailable", http.StatusServiceUnavailable)
			return
		}
		if artifact.Schema == "" || artifact.Fingerprint != schemaDigest(artifact.Schema) {
			http.Error(w, "invalid effective schema", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(artifact)
	})
}

// NewManifestHandler exposes one frozen runtime manifest for remote composition.
func NewManifestHandler(id string, endpoint manifestEndpoint, authenticate ManifestAuthenticator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authenticate == nil || !authenticate(r) {
			http.Error(w, "manifest authentication required", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if id == "" || endpoint == nil {
			http.Error(w, "manifest unavailable", http.StatusServiceUnavailable)
			return
		}
		snapshot, revision, err := publishedManifest(endpoint)
		if err != nil {
			http.Error(w, "manifest unavailable", http.StatusServiceUnavailable)
			return
		}
		manifest := Manifest{
			Version: endpointProtocolVersion, EndpointID: id, Schema: snapshot.Schema(), Fingerprint: snapshot.Fingerprint(),
			Capabilities: snapshot.Capabilities(),
		}
		manifest.Revision = revision
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
		etag := fmt.Sprintf(`"%d:%s"`, revision, snapshot.Fingerprint())
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_ = json.NewEncoder(w).Encode(manifest)
	})
}

func publishedManifest(endpoint manifestEndpoint) (*core.Snapshot, uint64, error) {
	if source, ok := endpoint.(manifestStateEndpoint); ok {
		return source.ManifestState()
	}
	snapshot, err := endpoint.Snapshot()
	return snapshot, 0, err
}

// RemoteOptions fixes every network boundary at startup; redirects are always denied.
type RemoteOptions struct {
	EndpointID, ManifestURL, EffectiveSchemaURL, ExecuteURL, SubscribeURL, HealthURL string
	Client                                                                           *http.Client
	Delegation                                                                       interface {
		Sign(context.Context, string) (string, error)
	}
	Timeout          time.Duration
	MaxResponseBytes int64
	AllowHTTP        bool
}

// RemoteEndpoint implements gql.Endpoint over GraphQL-over-HTTP.
type RemoteEndpoint struct {
	id        string
	manifest  string
	effective string
	execute   string
	client    *http.Client
	signer    interface {
		Sign(context.Context, string) (string, error)
	}
	maxResponse int64
	timeout     time.Duration
	subscribe   string
	health      string
	state       atomic.Pointer[remoteState]
	refresh     sync.Mutex
	historyMu   sync.RWMutex
	history     map[string]*remoteState
	order       []string
}

func (my *RemoteEndpoint) ControlState() core.ControlState {
	state := my.state.Load()
	if state == nil || state.snapshot == nil {
		return core.ControlState{}
	}
	return core.ControlState{Revision: state.revision, Fingerprint: state.snapshot.Fingerprint()}
}

// remoteState is published as one pointer so snapshot, subscription and
// health capability bits always describe the same manifest revision.
type remoteState struct {
	snapshot              *core.Snapshot
	revision              uint64
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
	return my.endpoint.healthFor(ctx, my.snapshot.Fingerprint())
}
func (my *remoteRevision) EffectiveSchema(ctx context.Context) (core.SchemaArtifact, error) {
	if my.endpoint.effective == "" {
		return staticSchemaArtifact(my.snapshot), nil
	}
	return my.endpoint.effectiveSchema(ctx, my.snapshot.Fingerprint())
}

func NewRemoteEndpoint(ctx context.Context, options RemoteOptions) (*RemoteEndpoint, error) {
	if options.EndpointID == "" || options.ManifestURL == "" || options.ExecuteURL == "" {
		return nil, fmt.Errorf("remote endpoint id, manifest URL and execute URL are required")
	}
	urls := []string{options.ManifestURL, options.ExecuteURL}
	if options.EffectiveSchemaURL != "" {
		urls = append(urls, options.EffectiveSchemaURL)
	}
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
		id: options.EndpointID, manifest: options.ManifestURL, effective: options.EffectiveSchemaURL, execute: options.ExecuteURL,
		client: client, signer: options.Delegation, maxResponse: options.MaxResponseBytes, timeout: options.Timeout, subscribe: options.SubscribeURL,
		health:  options.HealthURL,
		history: make(map[string]*remoteState, 8),
	}
	if err := remote.Refresh(ctx); err != nil {
		return nil, err
	}
	return remote, nil
}

// EffectiveSchema fetches an identity-bound schema artifact. It never falls
// back to the static manifest because doing so would leak or over-advertise
// fields hidden by the child policy.
func (my *RemoteEndpoint) EffectiveSchema(ctx context.Context) (core.SchemaArtifact, error) {
	state := my.state.Load()
	if state == nil || state.snapshot == nil {
		return core.SchemaArtifact{}, fmt.Errorf("remote manifest unavailable")
	}
	return my.effectiveSchema(ctx, state.snapshot.Fingerprint())
}

func (my *RemoteEndpoint) effectiveSchema(ctx context.Context, structural string) (core.SchemaArtifact, error) {
	if my == nil {
		return core.SchemaArtifact{}, fmt.Errorf("remote effective schema unavailable")
	}
	if my.effective == "" {
		state := my.state.Load()
		if state == nil || state.snapshot == nil || state.snapshot.Fingerprint() != structural {
			return core.SchemaArtifact{}, fmt.Errorf("remote manifest unavailable")
		}
		return staticSchemaArtifact(state.snapshot), nil
	}
	ctx, cancel := context.WithTimeout(ctx, my.timeout)
	defer cancel()
	reqPayload := request{}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, my.effective, nil)
	if err != nil {
		return core.SchemaArtifact{}, err
	}
	req.Header.Set("Accept", "application/json")
	if my.signer == nil {
		return core.SchemaArtifact{}, fmt.Errorf("remote effective schema requires delegation")
	}
	token, err := my.signer.Sign(ctx, operationDigest(reqPayload, "effective-schema"))
	if err != nil {
		return core.SchemaArtifact{}, err
	}
	req.Header.Set("Authorization", delegationScheme+token)
	resp, err := my.client.Do(req)
	if err != nil {
		return core.SchemaArtifact{}, fmt.Errorf("remote effective schema unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return core.SchemaArtifact{}, fmt.Errorf("remote effective schema status %d", resp.StatusCode)
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		return core.SchemaArtifact{}, fmt.Errorf("remote effective schema content type %q", mediaType)
	}
	var artifact core.SchemaArtifact
	if err := decodeResponse(resp.Body, my.maxResponse, &artifact); err != nil {
		return core.SchemaArtifact{}, err
	}
	if artifact.Schema == "" || artifact.Fingerprint == "" || artifact.StructuralFingerprint != structural {
		return core.SchemaArtifact{}, fmt.Errorf("remote effective schema is invalid")
	}
	if artifact.Fingerprint != schemaDigest(artifact.Schema) {
		return core.SchemaArtifact{}, fmt.Errorf("remote effective schema fingerprint mismatch")
	}
	return artifact, nil
}

func staticSchemaArtifact(snapshot *core.Snapshot) core.SchemaArtifact {
	return core.SchemaArtifact{Schema: snapshot.Schema(), Fingerprint: schemaDigest(snapshot.Schema()), StructuralFingerprint: snapshot.Fingerprint(), Capabilities: snapshot.Capabilities()}
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
func (my *RemoteEndpoint) ManifestRevision() uint64 {
	state := my.state.Load()
	if state == nil {
		return 0
	}
	return state.revision
}

func (my *RemoteEndpoint) ManifestState() (*core.Snapshot, uint64, error) {
	state := my.state.Load()
	if state == nil || state.snapshot == nil {
		return nil, 0, fmt.Errorf("remote manifest unavailable")
	}
	return state.snapshot, state.revision, nil
}

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
		my.historyMu.Lock()
		state = my.history[fingerprint]
		if state == nil {
			my.historyMu.Unlock()
			return nil, fmt.Errorf("remote snapshot revision unavailable")
		}
		for i, revision := range my.order {
			if revision == fingerprint {
				copy(my.order[i:], my.order[i+1:])
				my.order[len(my.order)-1] = fingerprint
				break
			}
		}
		my.historyMu.Unlock()
	}
	return &remoteRevision{endpoint: my, snapshot: state.snapshot, subscriptionAvailable: state.subscriptionAvailable, healthAvailable: state.healthAvailable}, nil
}

// Refresh atomically installs a fully validated manifest or retains the LKG snapshot.
func (my *RemoteEndpoint) Refresh(ctx context.Context) error {
	my.refresh.Lock()
	defer my.refresh.Unlock()
	ctx, cancel := context.WithTimeout(ctx, my.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, my.manifest, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if current := my.state.Load(); current != nil && current.snapshot != nil {
		req.Header.Set("If-None-Match", fmt.Sprintf(`"%d:%s"`, current.revision, current.snapshot.Fingerprint()))
	}
	if my.signer != nil {
		if _, ok := core.IdentityFromContext(ctx); ok {
			token, signErr := my.signer.Sign(ctx, operationDigest(request{}, "manifest"))
			if signErr != nil {
				return signErr
			}
			req.Header.Set("Authorization", delegationScheme+token)
		}
	}
	response, err := my.client.Do(req)
	if err != nil {
		return fmt.Errorf("remote manifest unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		return nil
	}
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
	current := my.state.Load()
	if current != nil {
		if manifest.Revision < current.revision {
			return fmt.Errorf("%w: %d", core.ErrControlStale, manifest.Revision)
		}
		if manifest.Revision != 0 && manifest.Revision == current.revision && current.snapshot.Fingerprint() != manifest.Fingerprint {
			return fmt.Errorf("%w: %d", core.ErrControlConflict, manifest.Revision)
		}
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
	next := &remoteState{snapshot: snapshot, revision: manifest.Revision, subscriptionAvailable: manifest.Subscription, healthAvailable: manifest.Health}
	if current != nil && current.snapshot.Fingerprint() != snapshot.Fingerprint() {
		my.historyMu.Lock()
		for index, revision := range my.order {
			if revision == snapshot.Fingerprint() {
				delete(my.history, revision)
				my.order = append(my.order[:index], my.order[index+1:]...)
				break
			}
		}
		fingerprint := current.snapshot.Fingerprint()
		my.history[fingerprint] = current
		my.order = append(my.order, fingerprint)
		if len(my.order) > 8 {
			delete(my.history, my.order[0])
			my.order = my.order[1:]
		}
		my.historyMu.Unlock()
	}
	my.state.Store(next)
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
		token, signErr := my.signer.Sign(ctx, operationDigest(payload, fingerprint))
		if signErr != nil {
			return nil, signErr
		}
		request.Header.Set("Authorization", delegationScheme+token)
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
	if fingerprint != "" && response.Header.Get("GQL-Snapshot") != fingerprint {
		return nil, fmt.Errorf("remote execute snapshot mismatch")
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
		token, signErr := my.signer.Sign(ctx, operationDigest(payload, fingerprint))
		if signErr != nil {
			return nil, signErr
		}
		request.Header.Set("Authorization", delegationScheme+token)
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
