package pgsql

import (
	"testing"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestBuildQueryEmptyPlaceholderIsConstant(t *testing.T) {
	ctx := compiler.NewContext(nil, `"`, nil)
	defer ctx.Release()
	field := &ast.Field{Name: "_empty", Alias: "_empty", Definition: &ast.FieldDefinition{Type: ast.NonNullNamedType("Boolean", nil)}}
	require.NoError(t, (&Dialect{}).BuildQuery(ctx, ast.SelectionSet{field}))
	require.Equal(t, `SELECT JSONB_BUILD_OBJECT('_empty', FALSE) AS "__root" FROM (SELECT TRUE) AS "__root_x"`, ctx.String())
}

func TestInsertDefaultOnlyUsesDefaultValues(t *testing.T) {
	class := &protocol.Class{
		Name: "DefaultOnly", Table: "defaults", PrimaryKeys: []string{"id"},
		Fields: map[string]*protocol.Field{
			"id":    {Name: "id", Column: "id", IsPrimary: true, Catalog: &protocol.Column{Generation: &protocol.ColumnGeneration{Kind: protocol.GeneratedIdentityByDefault}}},
			"title": {Name: "title", Column: "title", Catalog: &protocol.Column{Default: &protocol.ColumnDefault{Expression: "'ready'"}}},
		},
	}
	ctx := compiler.NewContext(testHoster{class: class}, `"`, nil)
	defer ctx.Release()
	columns, err := (&Dialect{}).writeInsertValues(ctx, class, []inputRow{{values: map[string]paramWriter{}}})
	require.NoError(t, err)
	require.Empty(t, columns)
	require.Equal(t, `INSERT INTO "defaults" DEFAULT VALUES`, ctx.String())
}
