package gql

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"
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
				body, err := marshalSubscriptionReply(ctx, reply)
				if err != nil {
					return
				}
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

func marshalSubscriptionReply(ctx context.Context, reply gqlReply) ([]byte, error) {
	if reply.encoded != nil {
		if limit := ResponseLimit(ctx); limit > 0 && int64(len(reply.encoded)) > limit {
			return marshalReply(ctx, gqlReply{Errors: GraphQLErrors(errResponseLimit)})
		}
		return append([]byte(nil), reply.encoded...), nil
	}
	body, err := marshalReply(ctx, reply)
	if err == nil {
		return body, nil
	}
	return marshalReply(ctx, gqlReply{Errors: GraphQLErrors(err)})
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
	if len(query) > maxDocumentBytes {
		return "", &publicFailure{"GraphQL document size limit exceeded", "LIMIT_EXCEEDED"}
	}
	if strings.TrimSpace(query) != "" {
		if my.options().PersistedOnly && !delegatedExecution(ctx) {
			return "", &publicFailure{"仅允许执行持久化操作", "PERSISTED_OPERATION_REQUIRED"}
		}
		return query, nil
	}
	if persisted, ok := my.documents[operationName]; ok {
		return persisted.query, nil
	}
	return "", &publicFailure{fmt.Sprintf("未找到名为'%s'的持久化操作", operationName), "PERSISTED_OPERATION_NOT_FOUND"}
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
	Degraded              bool
	DegradedSources       []string
	RouteScoped           bool
	SnapshotOK            bool
	SnapshotFingerprint   string
	ControlRevision       uint64
	RouteRevision         uint64
	SecurityRevision      uint64
	DatabaseOK            bool
	DatabaseError         string
	CDCConfigured         bool
	CDCState              string
	CDCConnected          bool
	CDCError              string
	SubscriptionAvailable bool
}

type healthResult struct {
	report HealthReport
	err    error
}

func collectHealth(names []string, check func(string) (HealthReport, error)) []healthResult {
	results := make([]healthResult, len(names))
	var group errgroup.Group
	group.SetLimit(8)
	for index, name := range names {
		group.Go(func() error {
			results[index].report, results[index].err = check(name)
			return nil
		})
	}
	_ = group.Wait()
	return results
}

type delegatedExecutionKey struct{}

func withDelegatedExecution(ctx context.Context) context.Context {
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
	if my != nil {
		if source, ok := my.revocation.(interface {
			Revision(context.Context) (uint64, error)
		}); ok {
			var err error
			report.SecurityRevision, err = source.Revision(ctx)
			if err != nil {
				return report, err
			}
		}
	}
	report.SubscriptionAvailable = report.CDCConfigured
	if report.CDCConfigured {
		report.CDCState, report.CDCError = my.cdc.cdcState()
		report.CDCConnected = report.CDCState != CDCStateDisconnected
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
		report.Ready = !report.CDCConfigured || report.CDCState != CDCStateDisconnected
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
	report.Ready = !report.CDCConfigured || report.CDCState != CDCStateDisconnected
	return report, nil
}

func mergeHealthCDC(report *HealthReport, child HealthReport) {
	if !child.CDCConfigured {
		return
	}
	report.CDCConfigured = true
	report.CDCConnected = report.CDCConnected && child.CDCConnected
	if child.CDCState == CDCStateDisconnected || (!child.CDCConnected && child.CDCState == "") {
		report.CDCState = CDCStateDisconnected
	} else if report.CDCState != CDCStateDisconnected && child.CDCState == CDCStateIdleValidated {
		report.CDCState = CDCStateIdleValidated
	}
}
