package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"

	core "github.com/ichaly/ideabase/gql"
)

type healthEndpoint interface {
	Health(context.Context) (core.HealthReport, error)
}

type healthStatus struct {
	Ready                 bool   `json:"ready"`
	SnapshotOK            bool   `json:"snapshotOk"`
	Snapshot              string `json:"snapshot,omitempty"`
	DatabaseOK            bool   `json:"databaseOk"`
	DatabaseError         string `json:"databaseError,omitempty"`
	CDCConfigured         bool   `json:"cdcConfigured"`
	CDCConnected          bool   `json:"cdcConnected"`
	CDCError              string `json:"cdcError,omitempty"`
	SubscriptionAvailable bool   `json:"subscriptionAvailable"`
}

func NewHealthHandler(endpoint healthEndpoint) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if endpoint == nil {
			http.Error(w, "health unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		report, err := endpoint.Health(r.Context())
		status := http.StatusOK
		if err != nil || !report.Ready {
			status = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(healthStatus{Ready: report.Ready, SnapshotOK: report.SnapshotOK, Snapshot: report.SnapshotFingerprint, DatabaseOK: report.DatabaseOK, DatabaseError: report.DatabaseError, CDCConfigured: report.CDCConfigured, CDCConnected: report.CDCConnected, CDCError: report.CDCError, SubscriptionAvailable: report.SubscriptionAvailable})
	})
}

func (my *RemoteEndpoint) Health(ctx context.Context) (core.HealthReport, error) {
	if my == nil || my.health == "" {
		return core.HealthReport{}, fmt.Errorf("remote health unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, my.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, my.health, nil)
	if err != nil {
		return core.HealthReport{}, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := my.client.Do(request)
	if err != nil {
		return core.HealthReport{}, fmt.Errorf("remote health unavailable: %w", err)
	}
	defer response.Body.Close()
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		return core.HealthReport{}, fmt.Errorf("remote health content type %q", mediaType)
	}
	var status healthStatus
	if err := decodeResponse(response.Body, my.maxResponse, &status); err != nil {
		return core.HealthReport{}, fmt.Errorf("remote health response: %w", err)
	}
	report := core.HealthReport{Ready: status.Ready, SnapshotOK: status.SnapshotOK, SnapshotFingerprint: status.Snapshot, DatabaseOK: status.DatabaseOK, DatabaseError: status.DatabaseError, CDCConfigured: status.CDCConfigured, CDCConnected: status.CDCConnected, CDCError: status.CDCError, SubscriptionAvailable: status.SubscriptionAvailable}
	state := my.state.Load()
	if state == nil || state.snapshot == nil || status.Snapshot != state.snapshot.Fingerprint() {
		report.Ready, report.SnapshotOK = false, false
		return report, fmt.Errorf("remote health snapshot mismatch")
	}
	if response.StatusCode != http.StatusOK || !status.Ready {
		return report, fmt.Errorf("remote endpoint is not ready")
	}
	return report, nil
}
