package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"time"

	core "github.com/ichaly/ideabase/gql"
)

const healthTimeout = 5 * time.Second
const healthMaxConcurrent = 8
const HealthProtocolVersion = 1

type healthStatus struct {
	Version               int      `json:"version"`
	Ready                 bool     `json:"ready"`
	Degraded              bool     `json:"degraded"`
	DegradedSources       []string `json:"degradedSources,omitempty"`
	RouteScoped           bool     `json:"routeScoped"`
	SnapshotOK            bool     `json:"snapshotOk"`
	Snapshot              string   `json:"snapshot,omitempty"`
	ControlRevision       uint64   `json:"controlRevision"`
	RouteRevision         uint64   `json:"routeRevision"`
	SecurityRevision      uint64   `json:"securityRevision"`
	DatabaseOK            bool     `json:"databaseOk"`
	DatabaseCode          string   `json:"databaseCode,omitempty"`
	CDCConfigured         bool     `json:"cdcConfigured"`
	CDCState              string   `json:"cdcState"`
	CDCConnected          bool     `json:"cdcConnected"`
	CDCCode               string   `json:"cdcCode,omitempty"`
	SubscriptionAvailable bool     `json:"subscriptionAvailable"`
}

func NewHealthHandler(endpoint core.HealthEndpoint) http.Handler {
	concurrency := make(chan struct{}, healthMaxConcurrent)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if endpoint == nil {
			writeProblem(w, http.StatusServiceUnavailable, "HEALTH_UNAVAILABLE", "health unavailable", true)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeProblem(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", false)
			return
		}
		select {
		case concurrency <- struct{}{}:
		default:
			writeProblem(w, http.StatusServiceUnavailable, "HEALTH_BUSY", "health check busy", true)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
		defer cancel()
		result := make(chan struct {
			report core.HealthReport
			err    error
		}, 1)
		go func() {
			defer func() { <-concurrency }()
			report, err := endpoint.Health(ctx)
			result <- struct {
				report core.HealthReport
				err    error
			}{report, err}
		}()
		var report core.HealthReport
		var err error
		select {
		case value := <-result:
			report, err = value.report, value.err
		case <-ctx.Done():
			err = ctx.Err()
		}
		if pinned := r.Header.Get("GQL-Snapshot"); pinned != "" && report.SnapshotFingerprint != pinned {
			report.Ready, report.SnapshotOK = false, false
			if err == nil {
				err = fmt.Errorf("health snapshot unavailable for pinned revision")
			}
		}
		status := http.StatusOK
		if err != nil || !report.Ready {
			status = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		databaseCode, cdcCode := "", ""
		if !report.DatabaseOK {
			databaseCode = "UNAVAILABLE"
		}
		if report.CDCConfigured && !report.CDCConnected {
			cdcCode = "DISCONNECTED"
		}
		_ = json.NewEncoder(w).Encode(healthStatus{Version: HealthProtocolVersion, Ready: report.Ready, Degraded: report.Degraded, DegradedSources: report.DegradedSources, RouteScoped: report.RouteScoped, SnapshotOK: report.SnapshotOK, Snapshot: report.SnapshotFingerprint, ControlRevision: report.ControlRevision, RouteRevision: report.RouteRevision, SecurityRevision: report.SecurityRevision, DatabaseOK: report.DatabaseOK, DatabaseCode: databaseCode, CDCConfigured: report.CDCConfigured, CDCState: report.CDCState, CDCConnected: report.CDCConnected, CDCCode: cdcCode, SubscriptionAvailable: report.SubscriptionAvailable})
	})
}

func (my *RemoteEndpoint) Health(ctx context.Context) (core.HealthReport, error) {
	state := my.state.Load()
	if state == nil || state.snapshot == nil {
		return core.HealthReport{}, fmt.Errorf("remote health snapshot unavailable")
	}
	return my.healthFor(ctx, state.snapshot.Fingerprint())
}

func (my *RemoteEndpoint) healthFor(ctx context.Context, expected string) (core.HealthReport, error) {
	if my == nil || my.health == "" {
		return core.HealthReport{}, fmt.Errorf("remote health unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, my.timeout)
	defer cancel()
	release, err := acquireRemoteSlot(ctx, my.unarySlots, "health")
	if err != nil {
		return core.HealthReport{}, err
	}
	defer release()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, my.health, nil)
	if err != nil {
		return core.HealthReport{}, err
	}
	request.Header.Set("Accept", "application/json")
	if expected != "" {
		request.Header.Set("GQL-Snapshot", expected)
	}
	response, err := my.client.Do(request)
	if err != nil {
		return core.HealthReport{}, fmt.Errorf("remote health unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusServiceUnavailable {
		return core.HealthReport{}, remoteResponseError(response, "health", my.maxResponse)
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		return core.HealthReport{}, fmt.Errorf("remote health content type %q", mediaType)
	}
	var status healthStatus
	if err := decodeResponse(response.Body, my.maxResponse, &status); err != nil {
		return core.HealthReport{}, fmt.Errorf("remote health response: %w", err)
	}
	if status.Version != 0 && status.Version != HealthProtocolVersion {
		return core.HealthReport{}, &RemoteProblemError{
			Operation: "health", Version: status.Version, Code: "UNSUPPORTED_HEALTH_VERSION",
			Category: "protocol", Message: "unsupported remote health version",
		}
	}
	report := core.HealthReport{Ready: status.Ready, Degraded: status.Degraded, DegradedSources: status.DegradedSources, RouteScoped: status.RouteScoped, SnapshotOK: status.SnapshotOK, SnapshotFingerprint: status.Snapshot, ControlRevision: status.ControlRevision, RouteRevision: status.RouteRevision, SecurityRevision: status.SecurityRevision, DatabaseOK: status.DatabaseOK, DatabaseError: status.DatabaseCode, CDCConfigured: status.CDCConfigured, CDCState: status.CDCState, CDCConnected: status.CDCConnected, CDCError: status.CDCCode, SubscriptionAvailable: status.SubscriptionAvailable}
	if expected != "" && status.Snapshot != expected {
		report.Ready, report.SnapshotOK = false, false
		return report, fmt.Errorf("remote health snapshot mismatch")
	}
	if response.StatusCode == http.StatusServiceUnavailable || !status.Ready {
		return report, &RemoteProblemError{
			Operation: "health", Version: HealthProtocolVersion, Code: "REMOTE_NOT_READY",
			Category: "availability", Retryable: true, Message: "remote endpoint is not ready",
		}
	}
	return report, nil
}
