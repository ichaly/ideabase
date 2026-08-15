package gql

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

const postgresPreflightTimeout = 5 * time.Second

func postgresPreflight(ctx context.Context, db *gorm.DB, name string, verify func(*gorm.DB) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, postgresPreflightTimeout)
	defer cancel()
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return fmt.Errorf("%s migration is unavailable: %w", name, tx.Error)
	}
	if err := tx.Exec(`SELECT set_config('lock_timeout', ?, true), set_config('statement_timeout', ?, true)`, postgresPreflightTimeout.String(), postgresPreflightTimeout.String()).Error; err != nil {
		_ = tx.Rollback().Error
		return fmt.Errorf("%s migration timeout setup: %w", name, err)
	}
	err := verify(tx)
	rollbackErr := tx.Rollback().Error
	if err != nil {
		return fmt.Errorf("%s migration is unavailable: %w", name, err)
	}
	if rollbackErr != nil {
		return fmt.Errorf("%s migration preflight rollback: %w", name, rollbackErr)
	}
	return nil
}
