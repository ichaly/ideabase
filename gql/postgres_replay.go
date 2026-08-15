package gql

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
)

const replayPruneBatch = 256

// PostgresReplayDDL is the shared delegation replay table migration.
// Applications apply it with their normal migration tool before serving.
const PostgresReplayDDL = `CREATE TABLE IF NOT EXISTS gql_delegation_replay (
  issuer text NOT NULL,
  audience text NOT NULL,
  token_id text NOT NULL,
  expires_at timestamptz NOT NULL,
  PRIMARY KEY (issuer, audience, token_id)
);
CREATE INDEX IF NOT EXISTS gql_delegation_replay_expiry
  ON gql_delegation_replay (expires_at);`

type postgresReplayStore struct {
	db        *gorm.DB
	nextPrune atomic.Int64
}

// NewPostgresReplayStore returns the production shared ReplayStore. It never
// creates schema at runtime; apply PostgresReplayDDL during deployment.
func NewPostgresReplayStore(db *gorm.DB) (ReplayStore, error) {
	if db == nil {
		return nil, fmt.Errorf("postgres replay store requires a database")
	}
	if err := postgresPreflight(context.Background(), db, "delegation replay", func(tx *gorm.DB) error {
		const key = `'gql-preflight-' || pg_backend_pid()::text || '-' || clock_timestamp()::text`
		if err := tx.Exec(`INSERT INTO gql_delegation_replay (issuer, audience, token_id, expires_at)
VALUES ('preflight', 'preflight', ` + key + `, clock_timestamp())`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE gql_delegation_replay SET expires_at=clock_timestamp()
WHERE issuer='preflight' AND audience='preflight' AND token_id LIKE 'gql-preflight-%'`).Error; err != nil {
			return err
		}
		result := tx.Exec(`DELETE FROM gql_delegation_replay
WHERE issuer='preflight' AND audience='preflight'
	  AND token_id LIKE 'gql-preflight-' || pg_backend_pid()::text || '-%'`)
		if result.Error != nil {
			return fmt.Errorf("delete delegation replay preflight probe: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("delete delegation replay preflight probe: expected one row, affected %d", result.RowsAffected)
		}
		var indexed bool
		if err := tx.Raw(`SELECT EXISTS (
  SELECT 1 FROM pg_indexes
  WHERE schemaname=current_schema() AND tablename='gql_delegation_replay'
    AND indexdef ILIKE '%(expires_at)%'
)`).Scan(&indexed).Error; err != nil {
			return err
		}
		if !indexed {
			return fmt.Errorf("expires_at index is unavailable")
		}
		return nil
	}); err != nil {
		return nil, err
	}
	store := &postgresReplayStore{db: db}
	store.nextPrune.Store(time.Now().Add(time.Minute).Unix())
	return store, nil
}

func (store *postgresReplayStore) Consume(ctx context.Context, key ReplayKey, expiry time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if key.Issuer == "" || key.Audience == "" || key.ID == "" || expiry.IsZero() {
		return fmt.Errorf("invalid replay record")
	}
	now, nextPrune := time.Now().Unix(), store.nextPrune.Load()
	if now >= nextPrune && store.nextPrune.CompareAndSwap(nextPrune, now+60) {
		result := store.db.WithContext(ctx).Exec(`DELETE FROM gql_delegation_replay WHERE ctid IN (
  SELECT ctid FROM gql_delegation_replay
  WHERE expires_at <= clock_timestamp()
  ORDER BY expires_at
  LIMIT ?
		)`, replayPruneBatch)
		if result.Error == nil && result.RowsAffected == replayPruneBatch {
			store.nextPrune.Store(now + 1)
		}
	}
	var consumed bool
	err := store.db.WithContext(ctx).Raw(`WITH accepted AS (
  INSERT INTO gql_delegation_replay (issuer, audience, token_id, expires_at)
  SELECT ?, ?, ?, ?
  WHERE ?::timestamptz > clock_timestamp()
  ON CONFLICT (issuer, audience, token_id) DO UPDATE
    SET expires_at = EXCLUDED.expires_at
    WHERE gql_delegation_replay.expires_at <= clock_timestamp()
  RETURNING 1
)
SELECT EXISTS (SELECT 1 FROM accepted)`, key.Issuer, key.Audience, key.ID, expiry, expiry).Scan(&consumed).Error
	if err != nil {
		return fmt.Errorf("consume delegation replay: %w", err)
	}
	if !consumed {
		return ErrReplay
	}
	return nil
}
