package gql

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const postgresPreflightTimeout = 5 * time.Second

func postgresPreflight(ctx context.Context, db *Database, name string, verify func(*sql.Tx) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, postgresPreflightTimeout)
	defer cancel()
	tx, err := db.Pool().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%s migration is unavailable: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('lock_timeout', $1, true), set_config('statement_timeout', $2, true)`, postgresPreflightTimeout.String(), postgresPreflightTimeout.String()); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("%s migration timeout setup: %w", name, err)
	}
	err = verify(tx)
	rollbackErr := tx.Rollback()
	if err != nil {
		return fmt.Errorf("%s migration is unavailable: %w", name, err)
	}
	if rollbackErr != nil {
		return fmt.Errorf("%s migration preflight rollback: %w", name, rollbackErr)
	}
	return nil
}
