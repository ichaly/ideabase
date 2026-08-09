package gql

import (
	"context"
	stdjson "encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

type nullSuccessResolver struct{}

func (nullSuccessResolver) Name() string { return "Query.requiredValue" }
func (nullSuccessResolver) Define() Define {
	return Define{Class: "Query", Name: "requiredValue", Result: "String!"}
}

func TestNullableRootResolverCompletionErrorHasFullPath(t *testing.T) {
	meta := emptyMetadata(t)
	compiler, err := NewCompiler(meta, nil)
	require.NoError(t, err)
	executor, err := NewExecutor(nil, NewRenderer(meta), meta, compiler)
	require.NoError(t, err)
	require.NoError(t, executor.Register(NewResolver("Query", "badInt", "", func(context.Context, Root, struct{}) (any, error) {
		return "not-an-int", nil
	}, Result("Int"))))

	body, err := executor.Execute(context.Background(), `{ alias: badInt }`, nil)
	require.NoError(t, err)
	var reply struct {
		Data   map[string]any `json:"data"`
		Errors gqlerror.List  `json:"errors"`
	}
	require.NoError(t, stdjson.Unmarshal(body, &reply))
	require.Nil(t, reply.Data["alias"])
	require.Len(t, reply.Errors, 1)
	require.Equal(t, ast.Path{ast.PathName("alias")}, reply.Errors[0].Path)
}
func (nullSuccessResolver) Resolve(context.Context, any, map[string]interface{}) (interface{}, error) {
	return nil, nil
}

// Handler success and failure share one GraphQL completion path. Returning nil
// without an error for a Non-Null field is still an execution error and must
// bubble to the root.
func TestRootResolverSuccessRunsGraphQLCompletion(t *testing.T) {
	meta := emptyMetadata(t)
	compiler, err := NewCompiler(meta, nil)
	require.NoError(t, err)
	executor, err := NewExecutor(nil, NewRenderer(meta), meta, compiler)
	require.NoError(t, err)
	require.NoError(t, executor.Register(nullSuccessResolver{}))

	body, err := executor.Execute(context.Background(), `{ requiredValue }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(body), "Cannot return null for non-nullable field Query.requiredValue")
	require.Contains(t, string(body), `"data":null`)
}
