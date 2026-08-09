package gql

import (
	"context"
	stdjson "encoding/json"
	"errors"
	"fmt"

	"github.com/vektah/gqlparser/v2/gqlerror"
)

// SubscriptionAvailable reports whether the executor has an executable CDC
// source for subscriptions.
func (my *Executor) SubscriptionAvailable() bool { return my != nil && my.cdc != nil }

// Subscribe exposes the transport-neutral subscription stream as JSON bytes.
// Each item is a complete GraphQL response and the returned channel is closed
// when the source ends or ctx is cancelled.
func (my *Executor) Subscribe(ctx context.Context, query string, variables map[string]interface{}, operationName string) (<-chan []byte, error) {
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

// Schema returns the frozen SDL contract used by this executor.
func (my *Executor) Schema() (string, error) {
	snapshot, err := my.Snapshot()
	if err != nil {
		return "", err
	}
	return string([]byte(snapshot.Schema())), nil
}

// ExecuteOperation executes one named operation from a multi-operation document.
func (my *Executor) ExecuteOperation(ctx context.Context, query string, variables map[string]any, operationName string) ([]byte, error) {
	return my.executeBytes(ctx, query, variables, operationName)
}

// Capabilities returns a deep copy of the frozen execution catalog.
func (my *Executor) Capabilities() ([]Capability, error) {
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
// reported as a capability and never makes an otherwise healthy executor fail.
func (my *Executor) Health(ctx context.Context) (HealthReport, error) {
	if ctx == nil {
		ctx = context.Background()
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
