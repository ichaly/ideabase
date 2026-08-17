package gql

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

func TestNullableRootResolverErrorContinuesWithPathAndLocation(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	require.NoError(t, executor.register(
		NewResolver[Root, struct{}, string]("Query", "failNullable", "", func(context.Context, Root, struct{}) (string, error) { return "", errors.New("boom") }, Result("String")),
		NewResolver[Root, struct{}, string]("Query", "okSibling", "", func(context.Context, Root, struct{}) (string, error) { return "ok", nil }, Result("String")),
	))
	reply := executor.run(context.Background(), "{ alias: failNullable okSibling }", nil, "")
	require.NotNil(t, reply.Data)
	require.Nil(t, reply.Data["alias"])
	require.Equal(t, "ok", reply.Data["okSibling"])
	require.Len(t, reply.Errors, 1)
	err := reply.Errors[0]
	require.Equal(t, "alias", err.Path.String())
	require.Equal(t, "resolver execution failed", err.Message)
	require.NotEmpty(t, err.Locations)
	require.Equal(t, "RESOLVER_ERROR", err.Extensions["code"])
}

func TestRLSQueryKeepsSuccessfulSiblingAfterNullableError(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	executor.rls = true
	require.NoError(t, executor.register(
		NewResolver[Root, struct{}, string]("Query", "rlsOK", "", func(context.Context, Root, struct{}) (string, error) { return "ok", nil }, Result("String")),
		NewResolver[Root, struct{}, string]("Query", "rlsFail", "", func(context.Context, Root, struct{}) (string, error) { return "", errors.New("hidden") }, Result("String")),
	))
	identity, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "u"})
	require.NoError(t, err)
	reply := executor.run(WithIdentity(context.Background(), identity), `{ rlsOK rlsFail }`, nil, "")
	require.Equal(t, "ok", reply.Data["rlsOK"])
	require.Nil(t, reply.Data["rlsFail"])
	require.Len(t, reply.Errors, 1)
}

func TestRootResolverPreservesNestedGQLErrorMetadata(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	require.NoError(t, executor.register(NewResolver[Root, struct{}, string]("Query", "metadataFailure", "", func(context.Context, Root, struct{}) (string, error) {
		return "", &gqlerror.Error{Message: "inner", Path: ast.Path{ast.PathName("nested")}, Locations: []gqlerror.Location{{Line: 9, Column: 4}}, Extensions: map[string]any{"code": "CUSTOM", "detail": "kept"}}
	}, Result("String"))))
	reply := executor.run(context.Background(), `{ shown: metadataFailure }`, nil, "")
	require.Len(t, reply.Errors, 1)
	require.Equal(t, "shown.nested", reply.Errors[0].Path.String())
	require.Equal(t, "CUSTOM", reply.Errors[0].Extensions["code"])
	require.Equal(t, "kept", reply.Errors[0].Extensions["detail"])
	require.Equal(t, 9, reply.Errors[0].Locations[0].Line)
}

func TestDefaultRootFailureUsesGenericExecutionCode(t *testing.T) {
	field := &ast.Field{
		Name: "users", Alias: "shown",
		Definition: &ast.FieldDefinition{Name: "users", Type: ast.NamedType("UserResult", nil)},
	}
	executor := &Engine{resolvers: map[string]Resolver{}}
	reply := executor.executeRootResolvers(context.Background(), &planEntry{operation: &ast.OperationDefinition{
		Operation: ast.Query, SelectionSet: ast.SelectionSet{field},
	}}, nil)
	require.Len(t, reply.Errors, 1)
	require.Equal(t, "shown", reply.Errors[0].Path.String())
	require.Equal(t, "EXECUTION_ERROR", reply.Errors[0].Extensions["code"])
}

func TestNonNullRootResolverErrorStopsAndMarshalsDataNull(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	called := false
	require.NoError(t, executor.register(
		NewResolver[Root, struct{}, string]("Query", "failNonNull", "", func(context.Context, Root, struct{}) (string, error) { return "", errors.New("boom") }, Result("String!")),
		NewResolver[Root, struct{}, string]("Query", "afterNonNull", "", func(context.Context, Root, struct{}) (string, error) { called = true; return "bad", nil }, Result("String")),
	))
	body, err := executor.Execute(context.Background(), `{ failNonNull afterNonNull }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(body), `"data":null`)
	require.False(t, called)
	invalid, err := executor.Execute(context.Background(), `{ doesNotExist }`, nil)
	require.NoError(t, err)
	require.NotContains(t, string(invalid), `"data"`)
}

func TestNullableMutationRootErrorContinuesButRollsBack(t *testing.T) {
	executor, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	insert := func(ctx context.Context, name string) error {
		return transactionFromContext(ctx).Exec("INSERT INTO users(name, email) VALUES (?, ?)", name, name+"@x").Error
	}
	require.NoError(t, executor.register(
		NewResolver[Root, struct{}, string]("Mutation", "firstWrite", "", func(ctx context.Context, _ Root, _ struct{}) (string, error) { return "first", insert(ctx, "first") }, Result("String")),
		NewResolver[Root, struct{}, string]("Mutation", "failWrite", "", func(ctx context.Context, _ Root, _ struct{}) (string, error) {
			if err := insert(ctx, "failed"); err != nil {
				return "", err
			}
			return "", errors.New("boom")
		}, Result("String")),
		NewResolver[Root, struct{}, string]("Mutation", "lastWrite", "", func(ctx context.Context, _ Root, _ struct{}) (string, error) { return "last", insert(ctx, "last") }, Result("String")),
	))
	reply := executor.run(context.Background(), `mutation { firstWrite failWrite lastWrite }`, nil, "")
	require.Nil(t, reply.Data, "rolled-back writes must not be reported as committed values")
	require.Len(t, reply.Errors, 1)
	require.Equal(t, true, reply.Errors[0].Extensions["transactionRolledBack"])
	var count int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM users WHERE name IN ('first', 'failed', 'last')").Scan(&count).Error)
	require.Zero(t, count)
}

func TestNonNullMutationRootErrorStopsAndRollsBack(t *testing.T) {
	executor, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	var afterCalls int
	insert := func(ctx context.Context, name string) error {
		return transactionFromContext(ctx).Exec("INSERT INTO users(name, email) VALUES (?, ?)", name, name+"@x").Error
	}
	require.NoError(t, executor.register(
		NewResolver[Root, struct{}, string]("Mutation", "nnFirst", "", func(ctx context.Context, _ Root, _ struct{}) (string, error) { return "first", insert(ctx, "nn-first") }, Result("String")),
		NewResolver[Root, struct{}, string]("Mutation", "nnFail", "", func(ctx context.Context, _ Root, _ struct{}) (string, error) {
			if err := insert(ctx, "nn-fail"); err != nil {
				return "", err
			}
			return "", errors.New("boom")
		}, Result("String!")),
		NewResolver[Root, struct{}, string]("Mutation", "nnAfter", "", func(context.Context, Root, struct{}) (string, error) { afterCalls++; return "bad", nil }, Result("String")),
	))
	reply := executor.run(context.Background(), `mutation { nnFirst nnFail nnAfter }`, nil, "")
	require.Nil(t, reply.Data)
	require.Zero(t, afterCalls)
	require.Equal(t, true, reply.Errors[0].Extensions["transactionRolledBack"])
	var count int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM users WHERE name IN ('nn-first', 'nn-fail')").Scan(&count).Error)
	require.Zero(t, count)
}
