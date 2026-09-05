package gql

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

func TestPublicWireContractsUseLowerCamelJSON(t *testing.T) {
	body, err := stdjson.Marshal(SchemaArtifact{Schema: "type Query{x:Int}", Fingerprint: "f", Capabilities: []Capability{{Coordinate: "Query.x", RetrySafe: true}}})
	require.NoError(t, err)
	require.JSONEq(t, `{"version":"","schema":"type Query{x:Int}","fingerprint":"f","structuralFingerprint":"","permissionSignature":"","policyRevision":0,"routeRevision":0,"executionPin":"","capabilities":[{"coordinate":"Query.x","parent":"","name":"","type":"","arguments":null,"handler":"","planner":"","actions":null,"resource":"","cost":0,"policyAware":false,"requires":null,"timeout":0,"retrySafe":true,"idempotencyKey":"","maxBatch":0}],"executionMode":""}`, string(body))
}

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

func TestGQLErrorsHideUnclassifiedInternalError(t *testing.T) {
	got := GraphQLErrors(fmt.Errorf("secret table users: invalid filter"))
	require.Equal(t, "internal execution error", got[0].Message)
	require.Equal(t, "EXECUTION_ERROR", got[0].Extensions["code"])
}

func TestGQLErrorsAddCodeToUncodedGraphQLError(t *testing.T) {
	got := GraphQLErrors(&gqlerror.Error{Message: "invalid selection"})
	require.Equal(t, "GRAPHQL_ERROR", got[0].Extensions["code"])
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

func TestSubscriptionSerializationFallbackHasStableCode(t *testing.T) {
	body, marshalErr := marshalSubscriptionReply(context.Background(), gqlReply{Data: map[string]any{"invalid": func() {}}})
	require.NoError(t, marshalErr)
	var reply gqlReply
	require.NoError(t, stdjson.Unmarshal(body, &reply))
	require.Equal(t, "EXECUTION_ERROR", reply.Errors[0].Extensions["code"])
}

func TestMarshalReplyAddsStableCodeToUnclassifiedGraphQLError(t *testing.T) {
	body, err := marshalReply(context.Background(), gqlReply{Errors: gqlerror.List{gqlerror.Errorf("bad value")}})
	require.NoError(t, err)
	require.JSONEq(t, `{"errors":[{"message":"bad value","extensions":{"code":"GRAPHQL_ERROR"}}]}`, string(body))
}
