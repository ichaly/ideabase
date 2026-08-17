package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const TransportDescriptorVersion = 1

// TransportDescriptor is the public zero-configuration discovery contract.
// Endpoint URLs may be absolute or relative to the descriptor URL.
type TransportDescriptor struct {
	Version            int    `json:"version"`
	EndpointID         string `json:"endpointId"`
	ManifestURL        string `json:"manifest"`
	EffectiveSchemaURL string `json:"effectiveSchema,omitempty"`
	ExecuteURL         string `json:"execute"`
	SubscribeURL       string `json:"subscribe,omitempty"`
	HealthURL          string `json:"health,omitempty"`
	DelegationScheme   string `json:"delegationScheme"`
	SnapshotHeader     string `json:"snapshotHeader"`
	ExecutionPinHeader string `json:"executionPinHeader"`
	ResponseMediaType  string `json:"responseMediaType"`
}

func NewTransportDescriptorHandler(descriptor TransportDescriptor) http.Handler {
	descriptor.Version = TransportDescriptorVersion
	descriptor.DelegationScheme = "GQL-Delegation"
	descriptor.SnapshotHeader = "GQL-Snapshot"
	descriptor.ExecutionPinHeader = executionPinHeader
	descriptor.ResponseMediaType = responseType
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeProblem(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", false)
			return
		}
		if descriptor.EndpointID == "" || descriptor.ManifestURL == "" || descriptor.ExecuteURL == "" {
			writeProblem(w, http.StatusServiceUnavailable, "DESCRIPTOR_UNAVAILABLE", "transport descriptor unavailable", true)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(descriptor)
	})
}

// DiscoverRemoteEndpoint resolves the versioned descriptor and constructs the
// same RemoteEndpoint used by explicit deployments.
func DiscoverRemoteEndpoint(ctx context.Context, descriptorURL string, options RemoteOptions) (*RemoteEndpoint, error) {
	if !remoteURLAllowed(descriptorURL, options.AllowHTTP) {
		return nil, fmt.Errorf("remote descriptor URL is not allowed: %s", descriptorURL)
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := options.Client
	if client == nil {
		client = &http.Client{}
	} else {
		copy := *client
		client = &copy
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, descriptorURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("remote descriptor: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, remoteResponseError(response, "descriptor", 64<<10)
	}
	var descriptor TransportDescriptor
	if err := decodeResponse(response.Body, 64<<10, &descriptor); err != nil {
		return nil, fmt.Errorf("remote descriptor: %w", err)
	}
	if descriptor.Version != TransportDescriptorVersion || strings.TrimSpace(descriptor.EndpointID) == "" ||
		descriptor.ManifestURL == "" || descriptor.ExecuteURL == "" ||
		descriptor.DelegationScheme != "GQL-Delegation" || descriptor.SnapshotHeader != "GQL-Snapshot" ||
		descriptor.ExecutionPinHeader != executionPinHeader || descriptor.ResponseMediaType != responseType {
		return nil, fmt.Errorf("remote descriptor identity or protocol mismatch")
	}
	base, err := url.Parse(descriptorURL)
	if err != nil {
		return nil, err
	}
	resolve := func(raw string) string {
		if raw == "" {
			return ""
		}
		reference, err := url.Parse(raw)
		if err != nil {
			return raw
		}
		return base.ResolveReference(reference).String()
	}
	for _, raw := range []string{descriptor.ManifestURL, descriptor.EffectiveSchemaURL, descriptor.ExecuteURL, descriptor.SubscribeURL, descriptor.HealthURL} {
		if raw == "" {
			continue
		}
		resolved, parseErr := url.Parse(resolve(raw))
		if parseErr != nil || !strings.EqualFold(resolved.Scheme, base.Scheme) || !strings.EqualFold(resolved.Host, base.Host) {
			return nil, fmt.Errorf("remote descriptor endpoint must use the descriptor origin")
		}
	}
	options.EndpointID = descriptor.EndpointID
	options.ManifestURL = resolve(descriptor.ManifestURL)
	options.EffectiveSchemaURL = resolve(descriptor.EffectiveSchemaURL)
	options.ExecuteURL = resolve(descriptor.ExecuteURL)
	options.SubscribeURL = resolve(descriptor.SubscribeURL)
	options.HealthURL = resolve(descriptor.HealthURL)
	options.Client = client
	return NewRemoteEndpoint(ctx, options)
}
