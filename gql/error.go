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
		return codedGraphQLErrors(list, "GRAPHQL_ERROR")
	}
	if item, ok := err.(*gqlerror.Error); ok {
		return codedGraphQLErrors(gqlerror.List{item}, "GRAPHQL_ERROR")
	}
	var list gqlerror.List
	if errors.As(err, &list) {
		return codedGraphQLErrors(list, "GRAPHQL_ERROR")
	}
	var item *gqlerror.Error
	if errors.As(err, &item) {
		return codedGraphQLErrors(gqlerror.List{item}, "GRAPHQL_ERROR")
	}
	if safe := safeFailure(err); safe != nil {
		err = safe
	}
	var public errorCoder
	if !errors.As(err, &public) {
		err = &publicFailure{"internal execution error", "EXECUTION_ERROR"}
	}
	item = gqlerror.Wrap(err)
	var coded errorCoder
	if errors.As(err, &coded) {
		item.Extensions = map[string]any{"code": coded.ErrorCode()}
	} else {
		item.Extensions = map[string]any{"code": "EXECUTION_ERROR"}
	}
	return gqlerror.List{item}
}

func resolverFailure(err error) error {
	var completion *nonNullResolverError
	if errors.As(err, &completion) {
		return completion.errors
	}
	var public errorCoder
	if safeFailure(err) != nil || errors.As(err, &public) || codedResolverError(err) {
		return err
	}
	return &publicFailure{"resolver execution failed", "RESOLVER_ERROR"}
}

func codedResolverError(err error) bool {
	var list gqlerror.List
	if errors.As(err, &list) {
		for _, item := range list {
			if item == nil || item.Extensions["code"] == nil {
				return false
			}
		}
		return len(list) > 0
	}
	var item *gqlerror.Error
	return errors.As(err, &item) && item.Extensions["code"] != nil
}

func codedGraphQLErrors(list gqlerror.List, fallback string) gqlerror.List {
	result := make(gqlerror.List, len(list))
	for i, item := range list {
		if item == nil {
			continue
		}
		clone := *item
		clone.Extensions = make(map[string]any, len(item.Extensions)+1)
		for key, value := range item.Extensions {
			clone.Extensions[key] = value
		}
		if clone.Extensions["code"] == nil {
			clone.Extensions["code"] = fallback
		}
		result[i] = &clone
	}
	return result
}

type publicFailure struct{ message, code string }

func (my *publicFailure) Error() string     { return my.message }
func (my *publicFailure) ErrorCode() string { return my.code }

func mutationDefinitelyRolledBack(err error) bool {
	var postgres *pgconn.PgError
	return errors.As(err, &postgres) || errors.Is(err, gorm.ErrDuplicatedKey) || errors.Is(err, gorm.ErrForeignKeyViolated)
}

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
