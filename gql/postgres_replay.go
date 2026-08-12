package gql

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
)

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
		if err := store.db.WithContext(ctx).Exec(`DELETE FROM gql_delegation_replay WHERE expires_at <= clock_timestamp()`).Error; err != nil {
			store.nextPrune.Store(now)
			return fmt.Errorf("prune delegation replay: %w", err)
		}
	}
	var consumed bool
	err := store.db.WithContext(ctx).Raw(`WITH accepted AS (
  INSERT INTO gql_delegation_replay (issuer, audience, token_id, expires_at)
  VALUES (?, ?, ?, ?)
  ON CONFLICT (issuer, audience, token_id) DO UPDATE
    SET expires_at = EXCLUDED.expires_at
    WHERE gql_delegation_replay.expires_at <= clock_timestamp()
  RETURNING 1
)
SELECT EXISTS (SELECT 1 FROM accepted)`, key.Issuer, key.Audience, key.ID, expiry).Scan(&consumed).Error
	if err != nil {
		return fmt.Errorf("consume delegation replay: %w", err)
	}
	if !consumed {
		return ErrReplay
	}
	return nil
}
