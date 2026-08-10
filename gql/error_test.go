package gql

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"gorm.io/gorm"
)

func TestGQLErrorsSanitizeDatabaseAndContextFailures(t *testing.T) {
	tests := []struct {
		name, code string
		err        error
	}{
		{name: "unique", code: "CONFLICT", err: &pgconn.PgError{Code: "23505", Message: "secret table users_email_key"}},
		{name: "foreign key", code: "FOREIGN_KEY_VIOLATION", err: fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "23503", Detail: "secret row"})},
		{name: "not null", code: "NOT_NULL_VIOLATION", err: &pgconn.PgError{Code: "23502"}},
		{name: "check", code: "CHECK_VIOLATION", err: &pgconn.PgError{Code: "23514"}},
		{name: "serialization", code: "SERIALIZATION_FAILURE", err: &pgconn.PgError{Code: "40001"}},
		{name: "deadlock", code: "DEADLOCK", err: &pgconn.PgError{Code: "40P01"}},
		{name: "statement timeout", code: "TIMEOUT", err: &pgconn.PgError{Code: "57014"}},
		{name: "gorm conflict", code: "CONFLICT", err: gorm.ErrDuplicatedKey},
		{name: "gorm foreign key", code: "FOREIGN_KEY_VIOLATION", err: gorm.ErrForeignKeyViolated},
		{name: "deadline", code: "TIMEOUT", err: context.DeadlineExceeded},
		{name: "cancel", code: "CANCELLED", err: context.Canceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := GraphQLErrors(test.err)
			require.Len(t, got, 1)
			require.Equal(t, test.code, got[0].Extensions["code"])
			require.NotContains(t, got[0].Message, "secret")
		})
	}
}

func TestGQLErrorsKeepOrdinaryPublicError(t *testing.T) {
	got := GraphQLErrors(fmt.Errorf("invalid filter"))
	require.Equal(t, "invalid filter", got[0].Message)
	require.Empty(t, got[0].Extensions)
}

func TestGQLErrorsPreserveStableCodeThroughWrapping(t *testing.T) {
	got := GraphQLErrors(fmt.Errorf("policy context: %w", &publicFailure{message: "denied", code: "FORBIDDEN"}))
	require.Equal(t, "FORBIDDEN", got[0].Extensions["code"])
}

func TestGraphQLErrorsPreserveWrappedGraphQLError(t *testing.T) {
	want := &gqlerror.Error{Message: "denied", Path: ast.Path{ast.PathName("account")}, Extensions: map[string]any{"code": "FORBIDDEN"}}
	got := GraphQLErrors(fmt.Errorf("resolver: %w", want))
	require.Equal(t, gqlerror.List{want}, got)
}
