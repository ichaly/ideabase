package gql

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestMergedRootFieldExecutesOnce(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	var calls atomic.Int32
	require.NoError(t, executor.register(NewResolver[Root, struct{}, string]("Query", "health", "", func(context.Context, Root, struct{}) (string, error) {
		calls.Add(1)
		return "ok", nil
	})))
	reply := executor.run(context.Background(), `query {
		...A
		...B
	}
	fragment A on Query { health }
	fragment B on Query { health }
	`, nil, "")
	require.Empty(t, reply.Errors)
	require.Equal(t, "ok", reply.Data["health"])
	require.EqualValues(t, 1, calls.Load())
}

func TestMergedDatabaseFieldKeepsAllChildSelections(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	reply := executor.run(context.Background(), `{ users(limit: 1) { items { id } } users(limit: 1) { total } }`, nil, "")
	require.Empty(t, reply.Errors)
	users := reply.Data["users"].(map[string]any)
	require.Contains(t, users, "items")
	require.Contains(t, users, "total")
}

func TestNormalizeSelectionMergesFieldsAfterFragmentsAndDirectives(t *testing.T) {
	schema := loadFrontendSchema(t, `
		type User { id: ID, name: String, email: String }
		type Query { user: User }
		schema { query: Query }
	`)
	doc, errs := gqlparser.LoadQuery(schema, `query($show: Boolean!) {
		user { id ...A ... on User { email } name @include(if: $show) }
	}
	fragment A on User { name id }
	`)
	require.Empty(t, errs)
	selection, err := normalizeSelectionWithSchema(doc.Operations[0].SelectionSet, doc.Fragments, map[string]any{"show": true}, schema)
	require.NoError(t, err)
	user := selection[0].(*ast.Field)
	require.Equal(t, []string{"id", "name", "email"}, fieldNames(user.SelectionSet))
	selection, err = normalizeSelectionWithSchema(doc.Operations[0].SelectionSet, doc.Fragments, map[string]any{"show": false}, schema)
	require.NoError(t, err)
	require.Equal(t, []string{"id", "name", "email"}, fieldNames(selection[0].(*ast.Field).SelectionSet), "fragment中的name仍应保留")
}

func TestNormalizeSelectionPreservesAbstractTypeConditions(t *testing.T) {
	schema := loadFrontendSchema(t, `
		interface Node { id: ID }
		type User implements Node { id: ID, name: String }
		type Query { node: Node }
		schema { query: Query }
	`)
	doc, errs := gqlparser.LoadQuery(schema, `query { node { id ... on User { name } } }`)
	require.Empty(t, errs)
	selection, err := normalizeSelectionWithSchema(doc.Operations[0].SelectionSet, doc.Fragments, nil, schema)
	require.NoError(t, err)
	node := selection[0].(*ast.Field)
	require.Equal(t, "id", node.SelectionSet[0].(*ast.Field).Name)
	require.Equal(t, "User", node.SelectionSet[1].(*ast.InlineFragment).TypeCondition)

	schema = loadFrontendSchema(t, `
		type User { id: ID, name: String, email: String }
		type Query { user: User }
		schema { query: Query }
	`)
	doc, errs = gqlparser.LoadQuery(schema, `{ user { email id } }`)
	require.Empty(t, errs)
	selection, err = normalizeSelectionWithSchema(doc.Operations[0].SelectionSet, nil, nil, schema)
	require.NoError(t, err)
	require.Equal(t, []string{"email", "id"}, fieldNames(selection[0].(*ast.Field).SelectionSet))
}

func TestNormalizeSelectionSkipsUnsupportedTypeConditionBeforeFlattening(t *testing.T) {
	schema := loadFrontendSchema(t, `
		interface Node { id: ID }
		type User implements Node { id: ID, name: String }
		type Query { node: Node }
		schema { query: Query }
	`)
	doc, errs := gqlparser.LoadQuery(schema, `query($skip: Boolean!) {
		node { id ... on User @skip(if: $skip) { name } }
	}`)
	require.Empty(t, errs)
	selection, err := normalizeSelectionWithSchema(doc.Operations[0].SelectionSet, nil, map[string]any{"skip": true}, schema)
	require.NoError(t, err)
	require.Equal(t, []string{"id"}, fieldNames(selection[0].(*ast.Field).SelectionSet))
}

func TestPrepareFrontendSharesCoercionAndSelectionNormalization(t *testing.T) {
	schema := loadFrontendSchema(t, `
		type User { id: ID, name: String }
		type Query { user: User }
		schema { query: Query }
	`)
	prepared, err := prepareFrontend(schema, `query Read($show: Boolean!) {
		user { ...UserFields }
	}
	fragment UserFields on User { id name @include(if: $show) }
	`, "Read", map[string]any{"show": true}, 256, nil)
	require.NoError(t, err)
	require.Equal(t, "Read", prepared.operation.Name)
	require.Equal(t, true, prepared.variables["show"])
	user := prepared.operation.SelectionSet[0].(*ast.Field)
	require.Equal(t, []string{"id", "name"}, fieldNames(user.SelectionSet))
}

func TestNormalizeSelectionTemplateDeepCopiesArgumentValues(t *testing.T) {
	schema := loadFrontendSchema(t, `type Query { user(id: ID): String }`)
	doc, errs := gqlparser.LoadQuery(schema, `{ user(id: "1") }`)
	require.Empty(t, errs)
	template := doc.Operations[0].SelectionSet
	copySet, err := normalizeSelectionTemplate(template, nil, nil, schema)
	require.NoError(t, err)
	meta := &Metadata{}
	meta.setCodecs([]Codec{idCodec{}})
	decodeLiterals(schema, copySet, meta)
	require.Equal(t, ast.IntValue, copySet[0].(*ast.Field).Arguments[0].Value.Kind)
	require.Equal(t, "1", copySet[0].(*ast.Field).Arguments[0].Value.Raw)
	require.Equal(t, ast.StringValue, template[0].(*ast.Field).Arguments[0].Value.Kind)
	require.Equal(t, "1", template[0].(*ast.Field).Arguments[0].Value.Raw)
}

func loadFrontendSchema(t *testing.T, input string) *ast.Schema {
	t.Helper()
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "frontend.graphql", Input: input})
	require.NoError(t, err)
	return schema
}

func fieldNames(set ast.SelectionSet) []string {
	out := make([]string, 0, len(set))
	for _, selection := range set {
		out = append(out, selection.(*ast.Field).Name)
	}
	return out
}
