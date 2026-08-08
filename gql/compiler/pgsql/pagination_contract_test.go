package pgsql

import (
	"testing"

	"github.com/ichaly/ideabase/gql/compiler"
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
