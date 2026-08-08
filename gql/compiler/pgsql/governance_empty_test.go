package pgsql

import (
	"testing"

	"github.com/ichaly/ideabase/gql/compiler"
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
