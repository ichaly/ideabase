package gql

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type runtimeTestNotifier struct{}

func (runtimeTestNotifier) watch(tables []string) (*watcher, error) {
	set := make(map[string]bool, len(tables))
	for _, table := range tables {
		set[table] = true
	}
	return &watcher{tables: set, wake: make(chan struct{}, 1)}, nil
}

func (runtimeTestNotifier) unwatch(*watcher)           {}
func (runtimeTestNotifier) invalidate(...string)       {}
func (runtimeTestNotifier) cdcState() (string, string) { return CDCStateConnected, "" }
func (runtimeTestNotifier) close()                     {}

type runtimeStatusNotifier struct{ connected atomic.Bool }

func (n *runtimeStatusNotifier) watch(tables []string) (*watcher, error) {
	return &watcher{tables: map[string]bool{}, wake: make(chan struct{}, 1), status: n.status}, nil
}
func (*runtimeStatusNotifier) unwatch(*watcher)     {}
func (*runtimeStatusNotifier) invalidate(...string) {}
func (*runtimeStatusNotifier) close()               {}
func (n *runtimeStatusNotifier) cdcState() (string, string) {
	if n.connected.Load() {
		return CDCStateConnected, ""
	}
	return CDCStateDisconnected, "CDC unavailable"
}
func (n *runtimeStatusNotifier) status() (bool, string) {
	if n.connected.Load() {
		return true, ""
	}
	return false, "CDC unavailable"
}

func TestExecutorRuntimeSubscribeEmitsJSONAndClosesOnCancel(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	if source := executor.cdc; source != nil {
		source.close()
	}
	executor.cdc = runtimeTestNotifier{}

	ctx, cancel := context.WithCancel(context.Background())
	events, err := executor.Subscribe(ctx, `subscription { users { total } }`, nil, "")
	require.NoError(t, err)
	select {
	case body, ok := <-events:
		require.True(t, ok)
		require.JSONEq(t, `{"data":{"users":{"total":0}}}`, string(body))
	case <-time.After(5 * time.Second):
		t.Fatal("等待订阅首帧超时")
	}
	cancel()
	select {
	case _, ok := <-events:
		require.False(t, ok)
	case <-time.After(5 * time.Second):
		t.Fatal("等待订阅通道关闭超时")
	}
}

func TestExecutorRuntimeSubscribeRejectsWithoutCDC(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	if source := executor.cdc; source != nil {
		source.close()
	}
	executor.cdc = nil
	_, err := executor.Subscribe(context.Background(), `subscription { users { total } }`, nil, "")
	require.Error(t, err)
	require.False(t, executor.SubscriptionAvailable())
}

func TestSubscriptionEncodedEventIsCopiedAndStillHonorsPerSubscriberLimit(t *testing.T) {
	reply := gqlReply{encoded: []byte(`{"data":{"value":"shared"}}`)}
	first, err := marshalSubscriptionReply(context.Background(), reply)
	require.NoError(t, err)
	first[0] = 'x'
	second, err := marshalSubscriptionReply(context.Background(), reply)
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"value":"shared"}}`, string(second))

	large := gqlReply{encoded: []byte(`{"data":{"value":"` + strings.Repeat("x", 256) + `"}}`)}
	limited, err := marshalSubscriptionReply(WithResponseLimit(context.Background(), 128), large)
	require.NoError(t, err)
	require.Contains(t, string(limited), `"code":"LIMIT_EXCEEDED"`)
}

func TestExecutorRuntimeSchemaAndCapabilitiesAreCopies(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()

	schema, err := executor.Schema()
	require.NoError(t, err)
	require.Contains(t, schema, "type Query")
	capabilities, err := executor.Capabilities()
	require.NoError(t, err)
	require.NotEmpty(t, capabilities)
	original := capabilities[0].Coordinate
	capabilities[0].Coordinate = "mutated"
	capabilities[0].Arguments = append(capabilities[0].Arguments, Argument{Name: "mutated"})
	fresh, err := executor.Capabilities()
	require.NoError(t, err)
	require.Equal(t, original, fresh[0].Coordinate)
	for _, argument := range fresh[0].Arguments {
		require.NotEqual(t, "mutated", argument.Name)
	}
}

func TestExecutorHealthReportsDatabaseFailure(t *testing.T) {
	executor, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	require.NotNil(t, executor)
	if source := executor.cdc; source != nil {
		source.close()
	}
	executor.cdc = nil
	_, err := executor.Snapshot()
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := executor.Health(ctx)
	require.Error(t, err)
	require.False(t, report.Ready)
	require.True(t, report.SnapshotOK)
	require.False(t, report.DatabaseOK)
	require.False(t, report.SubscriptionAvailable)
}

func TestExecutorHealthWithoutDatabaseStillReportsSnapshot(t *testing.T) {
	executor := &Engine{}
	report, err := executor.Health(context.Background())
	require.Error(t, err)
	require.False(t, report.Ready)
	require.False(t, report.SnapshotOK)
}

func TestExecutorHealthReadinessTracksCDCConnection(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	if source := executor.cdc; source != nil {
		source.close()
	}
	notifier := &runtimeStatusNotifier{}
	executor.cdc = notifier
	report, err := executor.Health(context.Background())
	require.NoError(t, err)
	require.True(t, report.CDCConfigured)
	require.False(t, report.CDCConnected)
	require.False(t, report.Ready)

	notifier.connected.Store(true)
	report, err = executor.Health(context.Background())
	require.NoError(t, err)
	require.True(t, report.CDCConnected)
	require.True(t, report.Ready)
}

func TestExecutorHealthReportsValidatedIdleCDCState(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	if source := executor.cdc; source != nil {
		source.close()
	}
	idle := newListener("", "")
	idle.setIdleValidated()
	defer idle.close()
	executor.cdc = idle

	report, err := executor.Health(context.Background())
	require.NoError(t, err)
	require.Equal(t, CDCStateIdleValidated, report.CDCState)
	require.True(t, report.CDCConnected)
	require.True(t, report.Ready)
}
