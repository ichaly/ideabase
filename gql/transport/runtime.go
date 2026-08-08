package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	core "github.com/ichaly/ideabase/gql"
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
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"`+snapshot.Fingerprint()+`"`)
		_ = json.NewEncoder(w).Encode(manifest)
	})
}

// RemoteOptions fixes every network boundary at startup; redirects are always denied.
type RemoteOptions struct {
	EndpointID, ManifestURL, ExecuteURL string
	Client                              *http.Client
	Delegation                          interface {
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
	snapshot    atomic.Pointer[core.Snapshot]
}

type remoteRevision struct {
	endpoint *RemoteEndpoint
	snapshot *core.Snapshot
}

func (my *remoteRevision) EndpointID() string                { return my.endpoint.id }
func (my *remoteRevision) Snapshot() (*core.Snapshot, error) { return my.snapshot, nil }
func (my *remoteRevision) Execute(ctx context.Context, query string, variables map[string]any) ([]byte, error) {
	return my.endpoint.executeOperation(ctx, query, variables, "", my.snapshot.Fingerprint())
}

func NewRemoteEndpoint(ctx context.Context, options RemoteOptions) (*RemoteEndpoint, error) {
	if options.EndpointID == "" || options.ManifestURL == "" || options.ExecuteURL == "" {
		return nil, fmt.Errorf("remote endpoint id, manifest URL and execute URL are required")
	}
	for _, raw := range []string{options.ManifestURL, options.ExecuteURL} {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(options.AllowHTTP && parsed.Scheme == "http")) {
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
	client.Timeout = options.Timeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	remote := &RemoteEndpoint{
		id: options.EndpointID, manifest: options.ManifestURL, execute: options.ExecuteURL,
		client: client, signer: options.Delegation, maxResponse: options.MaxResponseBytes,
	}
	if err := remote.Refresh(ctx); err != nil {
		return nil, err
	}
	return remote, nil
}

func (my *RemoteEndpoint) EndpointID() string { return my.id }

func (my *RemoteEndpoint) Snapshot() (*core.Snapshot, error) {
	snapshot := my.snapshot.Load()
	if snapshot == nil {
		return nil, fmt.Errorf("remote snapshot unavailable")
	}
	return snapshot, nil
}

func (my *RemoteEndpoint) ForSnapshot(fingerprint string) (core.Endpoint, error) {
	snapshot, err := my.Snapshot()
	if err != nil || snapshot.Fingerprint() != fingerprint {
		return nil, fmt.Errorf("remote snapshot revision unavailable")
	}
	return &remoteRevision{endpoint: my, snapshot: snapshot}, nil
}

// Refresh atomically installs a fully validated manifest or retains the LKG snapshot.
func (my *RemoteEndpoint) Refresh(ctx context.Context) error {
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
	snapshot, err := core.NewSnapshotArtifact(manifest.Schema, manifest.Fingerprint, manifest.Capabilities)
	if err != nil {
		return err
	}
	my.snapshot.Store(snapshot)
	return nil
}

func (my *RemoteEndpoint) Execute(ctx context.Context, query string, variables map[string]any) ([]byte, error) {
	return my.ExecuteOperation(ctx, query, variables, "")
}

func (my *RemoteEndpoint) ExecuteOperation(ctx context.Context, query string, variables map[string]any, operationName string) ([]byte, error) {
	return my.executeOperation(ctx, query, variables, operationName, "")
}

func (my *RemoteEndpoint) executeOperation(ctx context.Context, query string, variables map[string]any, operationName, fingerprint string) ([]byte, error) {
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
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remote execute status %d", response.StatusCode)
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType != responseType {
		return nil, fmt.Errorf("remote execute content type %q", mediaType)
	}
	return readLimited(response.Body, my.maxResponse)
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
