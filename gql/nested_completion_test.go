package gql

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestNullableNestedResolverErrorKeepsRowsAndReportsListIndex(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	var calls atomic.Int32
	require.NoError(t, executor.Register(NewResolver[Source, struct{}, string]("User", "risky", "", func(context.Context, Source, struct{}) (string, error) {
		if calls.Add(1) == 1 {
			return "", errors.New("nested boom")
		}
		return "ok", nil
	}, Result("String"))))
	seedNestedCompletionUsers(t, executor)

	reply := executor.run(context.Background(), `{ people: users(limit: 2) { items { id shown: risky } total } }`, nil, "")
	require.NotNil(t, reply.Data)
	require.Len(t, reply.Errors, 1)
	items := reply.Data["people"].(map[string]any)["items"].([]any)
	require.Len(t, items, 2)
	nilIndex := -1
	for i, item := range items {
		if item.(map[string]any)["shown"] == nil {
			nilIndex = i
		}
	}
	require.NotEqual(t, -1, nilIndex)
	require.Equal(t, "people.items["+strconv.Itoa(nilIndex)+"].shown", reply.Errors[0].Path.String())
	require.Equal(t, "RESOLVER_ERROR", reply.Errors[0].Extensions["code"])
	require.NotEmpty(t, reply.Errors[0].Locations)
}

func TestNonNullNestedResolverBubblesThroughGeneratedNonNullQuery(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	require.NoError(t, executor.Register(NewResolver[Source, struct{}, string]("User", "requiredValue", "", func(context.Context, Source, struct{}) (string, error) {
		return "", errors.New("required boom")
	}, Result("String!"))))
	seedNestedCompletionUsers(t, executor)

	const query = `{ users(limit: 1) { items { id requiredValue } total } }`
	reply := executor.run(context.Background(), query, nil, "")
	require.Nil(t, reply.Data)
	require.Len(t, reply.Errors, 1)
	require.Equal(t, "users.items[0].requiredValue", reply.Errors[0].Path.String())
	body, err := executor.Execute(context.Background(), query, nil)
	require.NoError(t, err)
	require.True(t, strings.Contains(string(body), `"data":null`), string(body))
}

func TestNonNullCompletionStopsAtNullableAncestor(t *testing.T) {
	rootField := &ast.Field{Alias: "container", Definition: &ast.FieldDefinition{Type: ast.NamedType("Container", nil)}}
	itemsField := &ast.Field{Alias: "items", Definition: &ast.FieldDefinition{Type: ast.NonNullListType(ast.NonNullNamedType("Item", nil), nil)}}
	required := &ast.Field{Alias: "required", Definition: &ast.FieldDefinition{Type: ast.NonNullNamedType("String", nil)}}
	binding := binding{Path: []string{"container", "items"}, Field: "required", node: required, trail: []*ast.Field{rootField, itemsField}}
	data := map[string]any{
		"container": map[string]any{"items": []any{map[string]any{"required": "value"}}},
		"sibling":   "kept",
	}
	refs := hostRefs(data, binding)
	require.Len(t, refs, 1)
	require.False(t, completeResolverFailure(refs[0], binding), "nullable container必须终止冒泡")
	require.Nil(t, data["container"])
	require.Equal(t, "kept", data["sibling"])
}

func seedNestedCompletionUsers(t *testing.T, executor *Executor) {
	t.Helper()
	reply := executor.run(context.Background(), `mutation {
		first: createUser(input: { name: "Nested A", email: "nested-a@example.com" }) { id }
		second: createUser(input: { name: "Nested B", email: "nested-b@example.com" }) { id }
	}`, nil, "")
	require.Empty(t, reply.Errors)
}
