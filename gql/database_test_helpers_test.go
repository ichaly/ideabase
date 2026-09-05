package gql

import (
	"context"
	"database/sql"
	"testing"
)

func testExec(t testing.TB, db *Database, query string, args ...any) sql.Result {
	t.Helper()
	result, err := db.Pool().ExecContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("execute SQL: %v", err)
	}
	return result
}

func testQueryRow(db *Database, query string, args ...any) *sql.Row {
	return db.Pool().QueryRowContext(context.Background(), query, args...)
}

func openTestDatabase(dsn string) (*Database, error) {
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db, err := NewDatabase(pool, "postgres", WithDatabaseDSN(dsn))
	if err != nil {
		_ = pool.Close()
		return nil, err
	}
	return db, nil
}

type testObserverFunc func(context.Context, Observation)

func (observe testObserverFunc) Observe(ctx context.Context, observation Observation) {
	observe(ctx, observation)
}
