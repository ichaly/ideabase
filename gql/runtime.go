package gql

import (
	"context"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/vektah/gqlparser/v2/gqlerror"
)

// SubscriptionAvailable reports whether the Engine has an executable CDC
// source for subscriptions.
func (my *Engine) SubscriptionAvailable() bool {
	return my != nil && my.cdc != nil
}

// Subscribe exposes the transport-neutral subscription stream as JSON bytes.
// Each item is a complete GraphQL response and the returned channel is closed
// when the source ends or ctx is cancelled.
func (my *Engine) Subscribe(ctx context.Context, query string, variables map[string]interface{}, operationName string) (<-chan []byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	query, err := my.operationDocument(ctx, query, operationName)
	if err != nil {
		return nil, err
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
	return snapshot.Schema(), nil
}

// ExecuteOperation executes one named operation from a multi-operation document.
func (my *Engine) ExecuteOperation(ctx context.Context, query string, variables map[string]any, operationName string) ([]byte, error) {
	query, err := my.operationDocument(ctx, query, operationName)
	if err != nil {
		return marshalGatewayError(err)
	}
	return my.executeBytes(ctx, query, variables, operationName)
}

func (my *Engine) operationDocument(ctx context.Context, query, operationName string) (string, error) {
	if my == nil {
		return "", errors.New("gql engine is unavailable")
	}
	if strings.TrimSpace(query) != "" {
		if my.options().PersistedOnly && !delegatedExecution(ctx) {
			return "", errors.New("仅允许执行持久化操作")
		}
		return query, nil
	}
	if persisted, ok := my.documents[operationName]; ok {
		return persisted.query, nil
	}
	return "", fmt.Errorf("未找到名为'%s'的持久化操作", operationName)
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
	CDCConnected          bool
	CDCError              string
	SubscriptionAvailable bool
}

type delegatedExecutionKey struct{}

// WithDelegatedExecution marks a document already admitted by a trusted gateway.
func WithDelegatedExecution(ctx context.Context) context.Context {
	return context.WithValue(ctx, delegatedExecutionKey{}, true)
}

func delegatedExecution(ctx context.Context) bool {
	trusted, _ := ctx.Value(delegatedExecutionKey{}).(bool)
	return trusted
}

// Health verifies the immutable contract and database connectivity. CDC is
// reported as a capability and never makes an otherwise healthy Engine fail.
func (my *Engine) Health(ctx context.Context) (HealthReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	report := HealthReport{CDCConfigured: my != nil && my.cdc != nil}
	report.SubscriptionAvailable = report.CDCConfigured
	if my != nil {
		if status, ok := my.cdc.(interface{ status() (bool, string) }); ok {
			report.CDCConnected, report.CDCError = status.status()
		} else if report.CDCConfigured {
			// Notifiers predating the optional status hook are assumed healthy.
			report.CDCConnected = true
		}
	}
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
		report.Ready = !report.CDCConfigured || report.CDCConnected
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
	report.Ready = !report.CDCConfigured || report.CDCConnected
	return report, nil
}
