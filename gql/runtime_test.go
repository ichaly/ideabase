package gql

import (
	"context"
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

func (runtimeTestNotifier) unwatch(*watcher) {}
func (runtimeTestNotifier) close()           {}

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
	executor := &Executor{}
	report, err := executor.Health(context.Background())
	require.Error(t, err)
	require.False(t, report.Ready)
	require.False(t, report.SnapshotOK)
}
