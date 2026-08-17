package gql

import (
	"context"
	stdjson "encoding/json"
	"testing"
	"time"

	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
)

func TestGABuildFailsClosedWhenCDCValidationFails(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	konfig, err := std.NewKonfig()
	require.NoError(t, err)
	konfig.Set("mode", "dev")
	konfig.Set("app.root", t.TempDir())
	konfig.Set("schema.schema", "public")
	konfig.Set("subscription.dsn", "host=127.0.0.1 port=1 user=x dbname=x sslmode=disable connect_timeout=1")
	_, err = New(konfig, db).testAllowAllPolicy().Build()
	require.ErrorContains(t, err, "CDC")
}

func TestGAPostgresLockTimeoutCancelsAndPoolRecovers(t *testing.T) {
	engine, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()

	lock := db.Begin()
	require.NoError(t, lock.Error)
	require.NoError(t, lock.Exec(`LOCK TABLE users IN ACCESS EXCLUSIVE MODE`).Error)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	body, err := engine.Execute(ctx, `{ users { total } }`, nil)
	require.NoError(t, err)
	require.Less(t, time.Since(started), time.Second)
	require.Contains(t, string(body), `"code":"TIMEOUT"`)
	require.NoError(t, lock.Rollback().Error)

	body, err = engine.Execute(context.Background(), `{ users { total } }`, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"users":{"total":0}}}`, string(body))
}

func TestGACDCReconnectConvergesAfterBackendTermination(t *testing.T) {
	engine, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := engine.Subscribe(ctx, `subscription { users { total } }`, nil, "")
	require.NoError(t, err)
	select {
	case body := <-events:
		var first gqlReply
		require.NoError(t, stdjson.Unmarshal(body, &first))
		require.Empty(t, first.Errors)
	case <-time.After(5 * time.Second):
		t.Fatal("missing initial subscription state")
	}

	var pid int
	require.Eventually(t, func() bool {
		return db.Raw(`SELECT pid FROM pg_stat_replication LIMIT 1`).Scan(&pid).Error == nil && pid != 0
	}, 5*time.Second, 50*time.Millisecond)
	require.NoError(t, db.Exec(`SELECT pg_terminate_backend(?)`, pid).Error)
	listener := engine.cdc.(*listener)
	require.Eventually(t, func() bool { connected, _ := listener.status(); return !connected }, time.Second, 10*time.Millisecond)
	require.NoError(t, db.Exec(`INSERT INTO users(name,email) VALUES ('Ada','ada@example.test')`).Error)

	select {
	case body := <-events:
		var update gqlReply
		require.NoError(t, stdjson.Unmarshal(body, &update))
		require.Empty(t, update.Errors)
		require.EqualValues(t, 1, update.Data["users"].(map[string]any)["total"])
	case <-time.After(10 * time.Second):
		t.Fatal("CDC did not reconnect and converge to the latest state")
	}
	connected, cdcError := listener.status()
	require.True(t, connected)
	require.Empty(t, cdcError)
}

func TestGACDCRejectsNarrowPublicationInsteadOfServingStaleStreams(t *testing.T) {
	engine, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	require.NoError(t, db.Exec(`DROP PUBLICATION ideabase_cdc`).Error)
	require.NoError(t, db.Exec(`CREATE PUBLICATION ideabase_cdc FOR TABLE users`).Error)
	_, err := engine.subscribe(context.Background(), `subscription { users { total } }`, nil, "")
	require.ErrorContains(t, err, "FOR ALL TABLES")
}
