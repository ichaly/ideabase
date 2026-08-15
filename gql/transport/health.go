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

type healthEndpoint interface {
	Health(context.Context) (core.HealthReport, error)
}

type healthStatus struct {
	Ready                 bool   `json:"ready"`
	SnapshotOK            bool   `json:"snapshotOk"`
	Snapshot              string `json:"snapshot,omitempty"`
	ControlRevision       uint64 `json:"controlRevision"`
	RouteRevision         uint64 `json:"routeRevision"`
	SecurityRevision      uint64 `json:"securityRevision"`
	DatabaseOK            bool   `json:"databaseOk"`
	DatabaseCode          string `json:"databaseCode,omitempty"`
	CDCConfigured         bool   `json:"cdcConfigured"`
	CDCConnected          bool   `json:"cdcConnected"`
	CDCCode               string `json:"cdcCode,omitempty"`
	SubscriptionAvailable bool   `json:"subscriptionAvailable"`
}

func NewHealthHandler(endpoint healthEndpoint) http.Handler {
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
		_ = json.NewEncoder(w).Encode(healthStatus{Ready: report.Ready, SnapshotOK: report.SnapshotOK, Snapshot: report.SnapshotFingerprint, ControlRevision: report.ControlRevision, RouteRevision: report.RouteRevision, SecurityRevision: report.SecurityRevision, DatabaseOK: report.DatabaseOK, DatabaseCode: databaseCode, CDCConfigured: report.CDCConfigured, CDCConnected: report.CDCConnected, CDCCode: cdcCode, SubscriptionAvailable: report.SubscriptionAvailable})
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
	if response.StatusCode != http.StatusOK {
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
	report := core.HealthReport{Ready: status.Ready, SnapshotOK: status.SnapshotOK, SnapshotFingerprint: status.Snapshot, ControlRevision: status.ControlRevision, RouteRevision: status.RouteRevision, SecurityRevision: status.SecurityRevision, DatabaseOK: status.DatabaseOK, DatabaseError: status.DatabaseCode, CDCConfigured: status.CDCConfigured, CDCConnected: status.CDCConnected, CDCError: status.CDCCode, SubscriptionAvailable: status.SubscriptionAvailable}
	if expected != "" && status.Snapshot != expected {
		report.Ready, report.SnapshotOK = false, false
		return report, fmt.Errorf("remote health snapshot mismatch")
	}
	if !status.Ready {
		return report, fmt.Errorf("remote endpoint is not ready")
	}
	return report, nil
}
