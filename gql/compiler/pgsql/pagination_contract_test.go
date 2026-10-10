package pgsql

import (
	"strings"
	"testing"

	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestCursorParamsRejectsCrossEntityAndOrderingBeforeSQL(t *testing.T) {
	contract := compiler.CursorContract{StructuralFingerprint: "shape-a", PolicyRevision: 4, Tenant: "tenant-a", Route: "route-a", Coordinate: "Query.users", Sort: []string{"name:ASC", "id:ASC"}}
	token, err := compiler.EncodeCursorToken(contract, []any{"Bob", int64(2)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := compiler.NewContext(nil, `"`, nil)
	ctx.SetCursorContract(contract)
	pager := &pager{keys: []pageKey{{column: "name"}, {column: "id"}}, cursor: &ast.Value{Raw: token, Kind: ast.StringValue}}
	if _, err := (&Dialect{}).cursorParams(ctx, pager); err != nil {
		t.Fatalf("matching cursor rejected: %v", err)
	}
	ctx.Release()

	ctx = compiler.NewContext(nil, `"`, nil)
	ctx.SetCursorContract(contract)
	pager.keys = []pageKey{{column: "id"}, {column: "name"}}
	if _, err := (&Dialect{}).cursorParams(ctx, pager); err == nil {
		t.Fatal("cursor with a different ordering must fail before SQL")
	}
	ctx.Release()
}

func TestPaginationIntConvertsIntegerTypes(t *testing.T) {
	for _, value := range []any{1, int64(2), int8(3), uint16(4)} {
		if got, ok := paginationInt(value); !ok || got < 1 {
			t.Fatalf("paginationInt(%#v) = %d, %v", value, got, ok)
		}
	}
	for _, value := range []any{1.5, "2", nil} {
		if _, ok := paginationInt(value); ok {
			t.Fatalf("paginationInt(%#v) unexpectedly accepted", value)
		}
	}
}

func TestVariablePaginationRangeMatchesLiteralValidation(t *testing.T) {
	class := &protocol.Class{Name: "User", PrimaryKeys: []string{"id"}, Fields: map[string]*protocol.Field{"id": {Name: "id", Column: "id"}}}
	for _, tc := range []struct {
		name string
		args ast.ArgumentList
		vars map[string]interface{}
		want string
	}{
		{"first", ast.ArgumentList{{Name: protocol.FIRST, Value: &ast.Value{Kind: ast.Variable, Raw: "n"}}}, map[string]interface{}{"n": 0}, "正整数"},
		{"last", ast.ArgumentList{{Name: protocol.LAST, Value: &ast.Value{Kind: ast.Variable, Raw: "n"}}}, map[string]interface{}{"n": -1}, "正整数"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := compiler.NewContext(nil, `"`, tc.vars)
			defer ctx.Release()
			_, err := newPager(ctx, scope{class: class}, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}

func (my *_DialectSuite) TestConnectionUsesEdgesAndPageInfoContract() {
	my.Require().NotNil(my.schema.Types["UserConnection"])
	my.Require().NotNil(my.schema.Types["UserEdge"])
	query := `query { userConnection(first: 2, sort: { name: ASC }) { edges { node { id name } cursor } pageInfo { hasNext hasPrev start end } totalCount } }`
	doc, gqlErr := gqlparser.LoadQuery(my.schema, query)
	my.Require().Empty(gqlErr)
	compile, err := gql.NewCompiler(my.meta, []compiler.Dialect{my.dialect})
	my.Require().NoError(err)
	sql, _, err := compile.Build(doc.Operations[0], nil)
	my.Require().NoError(err)
	my.Contains(sql, "'edges'")
	my.Contains(sql, "'node'")
	my.Contains(sql, "'cursor'")
	my.Contains(sql, "'pageInfo'")
	my.Contains(sql, "'totalCount'")
}
