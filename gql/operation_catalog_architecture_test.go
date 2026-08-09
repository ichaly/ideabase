package gql

import (
	"testing"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// The operation catalog owns mutation meaning. The SQL compiler must not
// recover that meaning from create/update/delete field-name prefixes.
func TestCompilerUsesCanonicalOperationForRenamedMutationCoordinate(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()

	class, ok := executor.metadata.GetNode("User")
	require.True(t, ok)
	executor.metadata.operations["Mutation.saveUser"] = operationCapability{
		class: class, actions: []string{protocol.OP_INSERT}, operation: protocol.CREATE,
	}
	field := &ast.Field{
		Name: "saveUser", Alias: "saveUser",
		Arguments: ast.ArgumentList{{
			Name: protocol.INPUT,
			Value: &ast.Value{Kind: ast.ObjectValue, Children: ast.ChildValueList{
				{Name: "name", Value: &ast.Value{Kind: ast.StringValue, Raw: "canonical"}},
			}},
		}},
		ObjectDefinition: &ast.Definition{Name: "Mutation", Kind: ast.Object},
	}
	operation := &ast.OperationDefinition{Operation: ast.Mutation, SelectionSet: ast.SelectionSet{field}}

	plan, err := executor.compiler.Compile(operation, nil)
	require.NoError(t, err)
	require.Contains(t, plan.SQL, `INSERT INTO `)
	require.Contains(t, plan.SQL, `"users"`)
}

// Root SDL is a projection of the already-built operation catalog. Renderer
// must not independently rediscover an operation from Class flags.
func TestRendererUsesCanonicalOperationCatalog(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	delete(executor.metadata.operations, "Mutation.createUser")

	source, err := NewRenderer(executor.metadata).Render()
	require.NoError(t, err)
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "canonical.graphql", Input: source})
	require.NoError(t, err)
	require.Nil(t, schema.Mutation.Fields.ForName("createUser"))
}
