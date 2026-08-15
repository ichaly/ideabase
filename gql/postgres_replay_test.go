package gql

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPostgresReplayStoreIsAtomicAcrossInstances(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	require.NoError(t, db.Exec(PostgresReplayDDL).Error)
	one, err := NewPostgresReplayStore(db)
	require.NoError(t, err)
	two, err := NewPostgresReplayStore(db)
	require.NoError(t, err)

	key := ReplayKey{Issuer: "gateway", Audience: "child", ID: "same-token"}
	var accepted atomic.Int32
	var group sync.WaitGroup
	for index := range 64 {
		group.Add(1)
		go func() {
			defer group.Done()
			store := one
			if index%2 == 0 {
				store = two
			}
			err := store.Consume(context.Background(), key, time.Now().Add(time.Minute))
			if err == nil {
				accepted.Add(1)
				return
			}
			require.ErrorIs(t, err, ErrReplay)
		}()
	}
	group.Wait()
	require.EqualValues(t, 1, accepted.Load())
}

func TestPostgresReplayStoreFailsClosedWhenTableIsUnavailable(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	require.NoError(t, db.Exec(`DROP TABLE IF EXISTS gql_delegation_replay`).Error)
	_, err := NewPostgresReplayStore(db)
	require.ErrorContains(t, err, "migration")
	require.False(t, errors.Is(err, ErrReplay))
}

func TestPostgresReplayStoreFailsClosedForMalformedMigration(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	require.NoError(t, db.Exec(`CREATE TABLE gql_delegation_replay (token_id text PRIMARY KEY)`).Error)
	_, err := NewPostgresReplayStore(db)
	require.ErrorContains(t, err, "migration")
}

func TestPostgresReplayStorePrunesExpiredRecordsOffTheHotPath(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	require.NoError(t, db.Exec(PostgresReplayDDL).Error)
	require.NoError(t, db.Exec(`INSERT INTO gql_delegation_replay VALUES ('old', 'old', 'old', clock_timestamp() - interval '1 minute')`).Error)
	store, err := NewPostgresReplayStore(db)
	require.NoError(t, err)
	store.(*postgresReplayStore).nextPrune.Store(0)
	require.NoError(t, store.Consume(context.Background(), ReplayKey{Issuer: "new", Audience: "new", ID: "new"}, time.Now().Add(time.Minute)))
	var count int64
	require.NoError(t, db.Table("gql_delegation_replay").Where("issuer = ?", "old").Count(&count).Error)
	require.Zero(t, count)
}

func TestPostgresReplayStoreContinuesWhenMaintenancePruneFails(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	require.NoError(t, db.Exec(PostgresReplayDDL).Error)
	store, err := NewPostgresReplayStore(db)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE FUNCTION reject_replay_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'maintenance unavailable'; END $$`).Error)
	require.NoError(t, db.Exec(`CREATE TRIGGER reject_replay_delete BEFORE DELETE ON gql_delegation_replay FOR EACH STATEMENT EXECUTE FUNCTION reject_replay_delete()`).Error)
	postgres := store.(*postgresReplayStore)
	postgres.nextPrune.Store(0)
	require.NoError(t, postgres.Consume(context.Background(), ReplayKey{Issuer: "new", Audience: "new", ID: "fresh"}, time.Now().Add(time.Minute)))
	require.Greater(t, postgres.nextPrune.Load(), time.Now().Unix())
}

func TestPostgresReplayStoreBoundsEachPruneBatch(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	require.NoError(t, db.Exec(PostgresReplayDDL).Error)
	require.NoError(t, db.Exec(`INSERT INTO gql_delegation_replay (issuer, audience, token_id, expires_at)
		SELECT 'old', 'old', generate_series(1, 300)::text, clock_timestamp() - interval '1 minute'`).Error)
	store, err := NewPostgresReplayStore(db)
	require.NoError(t, err)
	store.(*postgresReplayStore).nextPrune.Store(0)
	require.NoError(t, store.Consume(context.Background(), ReplayKey{Issuer: "new", Audience: "new", ID: "new"}, time.Now().Add(time.Minute)))
	var remaining int64
	require.NoError(t, db.Table("gql_delegation_replay").Where("issuer = ?", "old").Count(&remaining).Error)
	require.Positive(t, remaining, "one authentication must not prune an unbounded table")
}

func TestPostgresReplayStoreRejectsExpiryAtOrBeforeDatabaseClock(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	require.NoError(t, db.Exec(PostgresReplayDDL).Error)
	store, err := NewPostgresReplayStore(db)
	require.NoError(t, err)
	key := ReplayKey{Issuer: "gateway", Audience: "child", ID: "expired"}
	require.ErrorIs(t, store.Consume(context.Background(), key, time.Unix(1, 0)), ErrReplay)
	require.NoError(t, store.Consume(context.Background(), key, time.Now().Add(time.Minute)))
}
