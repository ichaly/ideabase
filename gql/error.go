package gql

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"gorm.io/gorm"
)

type errorCoder interface{ ErrorCode() string }

// GraphQLErrors converts one execution failure into the engine's public,
// transport-independent GraphQL error contract.
func GraphQLErrors(err error) gqlerror.List {
	if err == nil {
		return nil
	}
	if list, ok := err.(gqlerror.List); ok {
		return list
	}
	if item, ok := err.(*gqlerror.Error); ok {
		return gqlerror.List{item}
	}
	var list gqlerror.List
	if errors.As(err, &list) {
		return list
	}
	var item *gqlerror.Error
	if errors.As(err, &item) {
		return gqlerror.List{item}
	}
	if safe := safeFailure(err); safe != nil {
		err = safe
	}
	item = gqlerror.Wrap(err)
	var coded errorCoder
	if errors.As(err, &coded) {
		item.Extensions = map[string]any{"code": coded.ErrorCode()}
	}
	return gqlerror.List{item}
}

type publicFailure struct{ message, code string }

func (my *publicFailure) Error() string     { return my.message }
func (my *publicFailure) ErrorCode() string { return my.code }

func safeFailure(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &publicFailure{"operation timed out", "TIMEOUT"}
	}
	if errors.Is(err, context.Canceled) {
		return &publicFailure{"operation cancelled", "CANCELLED"}
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return &publicFailure{"resource conflicts with existing data", "CONFLICT"}
	}
	if errors.Is(err, gorm.ErrForeignKeyViolated) {
		return &publicFailure{"referenced resource is invalid", "FOREIGN_KEY_VIOLATION"}
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &publicFailure{"resource not found", "NOT_FOUND"}
	}
	var postgres *pgconn.PgError
	if !errors.As(err, &postgres) {
		return nil
	}
	switch postgres.Code {
	case "23505":
		return &publicFailure{"resource conflicts with existing data", "CONFLICT"}
	case "23503":
		return &publicFailure{"referenced resource is invalid", "FOREIGN_KEY_VIOLATION"}
	case "23502":
		return &publicFailure{"required value is missing", "NOT_NULL_VIOLATION"}
	case "23514":
		return &publicFailure{"value violates a data constraint", "CHECK_VIOLATION"}
	case "40001":
		return &publicFailure{"transaction could not be serialized", "SERIALIZATION_FAILURE"}
	case "40P01":
		return &publicFailure{"transaction deadlock detected", "DEADLOCK"}
	case "55P03":
		return &publicFailure{"database lock timed out", "LOCK_TIMEOUT"}
	case "57014":
		return &publicFailure{"database operation timed out", "TIMEOUT"}
	default:
		return &publicFailure{"database operation failed", "DATABASE_ERROR"}
	}
}
