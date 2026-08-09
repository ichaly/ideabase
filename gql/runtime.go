package gql

import (
	"context"
	stdjson "encoding/json"
	"errors"
	"fmt"

	"github.com/vektah/gqlparser/v2/gqlerror"
)

// SubscriptionAvailable reports whether the Engine has an executable CDC
// source for subscriptions.
func (my *Engine) SubscriptionAvailable() bool {
	if my == nil {
		return false
	}
	if my.tenantRouter == nil {
		return my.cdc != nil
	}
	if len(my.tenantSources) == 0 {
		return false
	}
	for _, source := range my.tenantSources {
		if !source.SubscriptionAvailable() {
			return false
		}
	}
	return true
}

// Subscribe exposes the transport-neutral subscription stream as JSON bytes.
// Each item is a complete GraphQL response and the returned channel is closed
// when the source ends or ctx is cancelled.
func (my *Engine) Subscribe(ctx context.Context, query string, variables map[string]interface{}, operationName string) (<-chan []byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	source, err := my.subscribe(ctx, query, variables, operationName)
	if err != nil {
		return nil, err
	}
	events := make(chan []byte, 1)
	go func() {
		defer close(events)
		for {
			select {
			case <-ctx.Done():
				return
			case reply, ok := <-source:
				if !ok {
					return
				}
				body := marshalSubscriptionReply(reply)
				select {
				case events <- body:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return events, nil
}

func marshalSubscriptionReply(reply gqlReply) []byte {
	body, err := stdjson.Marshal(reply)
	if err == nil {
		return body
	}
	fallback := gqlReply{Errors: gqlerror.List{gqlerror.Wrap(errors.New("subscription response serialization failed"))}}
	body, _ = stdjson.Marshal(fallback)
	return body
}

// Schema returns the frozen SDL contract used by this Engine.
func (my *Engine) Schema() (string, error) {
	snapshot, err := my.Snapshot()
	if err != nil {
		return "", err
	}
	return string([]byte(snapshot.Schema())), nil
}

// ExecuteOperation executes one named operation from a multi-operation document.
func (my *Engine) ExecuteOperation(ctx context.Context, query string, variables map[string]any, operationName string) ([]byte, error) {
	return my.executeBytes(ctx, query, variables, operationName)
}

// Capabilities returns a deep copy of the frozen Engine catalog.
func (my *Engine) Capabilities() ([]Capability, error) {
	snapshot, err := my.Snapshot()
	if err != nil {
		return nil, err
	}
	return snapshot.Capabilities(), nil
}

// HealthReport is a read-only runtime readiness view.
type HealthReport struct {
	Ready                 bool
	SnapshotOK            bool
	SnapshotFingerprint   string
	DatabaseOK            bool
	DatabaseError         string
	CDCConfigured         bool
	SubscriptionAvailable bool
}

// Health verifies the immutable contract and database connectivity. CDC is
// reported as a capability and never makes an otherwise healthy Engine fail.
func (my *Engine) Health(ctx context.Context) (HealthReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if my != nil && my.tenantRouter != nil {
		return my.tenantHealth(ctx)
	}
	report := HealthReport{CDCConfigured: my != nil && my.cdc != nil}
	report.SubscriptionAvailable = report.CDCConfigured
	if my == nil || (my.schema == nil && my.snapshot == nil) {
		return report, errors.New("gql schema is unavailable")
	}
	snapshot, err := my.Snapshot()
	if err != nil {
		return report, err
	}
	report.SnapshotOK = true
	report.SnapshotFingerprint = snapshot.Fingerprint()
	if my.database == nil {
		report.DatabaseOK = true
		report.Ready = true
		return report, nil
	}
	sqlDB, err := my.database.DB()
	if err == nil {
		err = sqlDB.PingContext(ctx)
	}
	if err != nil {
		report.DatabaseError = fmt.Sprint(err)
		return report, err
	}
	report.DatabaseOK = true
	report.Ready = true
	return report, nil
}

func (my *Engine) tenantHealth(ctx context.Context) (HealthReport, error) {
	report := HealthReport{Ready: true, SnapshotOK: my.snapshot != nil, DatabaseOK: true, CDCConfigured: true, SubscriptionAvailable: true}
	if my.snapshot != nil {
		report.SnapshotFingerprint = my.snapshot.Fingerprint()
	}
	for _, name := range sortedTenantSources(my.tenantSources) {
		child, err := my.tenantSources[name].Health(ctx)
		report.DatabaseOK = report.DatabaseOK && child.DatabaseOK
		report.CDCConfigured = report.CDCConfigured && child.CDCConfigured
		report.SubscriptionAvailable = report.SubscriptionAvailable && child.SubscriptionAvailable
		if err != nil || !child.Ready {
			report.Ready = false
			return report, fmt.Errorf("tenant datasource %s is not ready", name)
		}
	}
	return report, nil
}
