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
	store, err := NewPostgresReplayStore(db)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`DROP TABLE IF EXISTS gql_delegation_replay`).Error)
	err = store.Consume(context.Background(), ReplayKey{Issuer: "i", Audience: "a", ID: "j"}, time.Now().Add(time.Minute))
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrReplay))
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
