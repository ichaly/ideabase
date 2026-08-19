package compiler

import (
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
)

func TestFieldsOfReturnsOnlyFields(t *testing.T) {
	set := ast.SelectionSet{
		&ast.Field{Name: "one"},
		&ast.InlineFragment{SelectionSet: ast.SelectionSet{&ast.Field{Name: "nested"}}},
		&ast.Field{Name: "two"},
	}
	fields := FieldsOf(set)
	if len(fields) != 2 || fields[0].Name != "one" || fields[1].Name != "two" {
		t.Fatalf("fields = %#v", fields)
	}
}
