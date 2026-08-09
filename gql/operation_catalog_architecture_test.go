package gql

import (
	"strings"
	"testing"

	"github.com/ichaly/ideabase/gql/compiler"
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
		class: class, actions: []string{protocol.OP_INSERT}, operation: protocol.CREATE, shape: compiler.ShapeSingle,
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

// Every built-in root field is executable through the same exact-coordinate
// descriptor that the renderer projected into SDL. Inputs are part of that
// projection too: removing an operation must remove its now-unreachable input.
func TestBuiltInOperationSDLHasUniqueExecutableDescriptor(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()

	delete(executor.metadata.operations, "Mutation.createUser")
	delete(executor.metadata.operations, "Mutation.createUsers")
	delete(executor.metadata.operations, "Mutation.updateUserByPk")
	source, err := NewRenderer(executor.metadata).Render()
	require.NoError(t, err)
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "catalog.graphql", Input: source})
	require.NoError(t, err)

	for _, root := range []*ast.Definition{schema.Query, schema.Mutation, schema.Subscription} {
		if root == nil {
			continue
		}
		for _, field := range root.Fields {
			if field.Name == "_empty" || strings.HasPrefix(field.Name, "__") {
				continue
			}
			coordinate := root.Name + "." + field.Name
			spec, ok := executor.metadata.LookupOperation(coordinate)
			require.Truef(t, ok, "SDL root field %s must have a catalog descriptor", coordinate)
			require.NotNil(t, spec.Class, coordinate)
			require.NotEmpty(t, spec.Operation, coordinate)
		}
	}
	require.Nil(t, schema.Mutation.Fields.ForName("createUser"))
	require.Nil(t, schema.Types["UserCreateInput"], "unreachable create input must not be exposed")
}
