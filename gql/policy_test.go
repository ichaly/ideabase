package gql

import (
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2/ast"
	"testing"
)

func TestClassForMutationScalarResolvesTarget(t *testing.T) {
	m := &Metadata{Nodes: map[string]*protocol.Class{"User": {Name: "User", Table: "users", PrimaryKeys: []string{"id"}, Fields: map[string]*protocol.Field{"id": {Name: "id", Column: "id", IsPrimary: true}}}}}
	m.operations = buildOperationCatalog(m)
	f := &ast.Field{Name: "deleteUserByPk", Definition: &ast.FieldDefinition{Type: ast.NamedType("Int", nil)}}
	c, action := classForField(m, f)
	if c == nil || c.Name != "User" || action != protocol.OP_DELETE {
		t.Fatalf("target=%v action=%s", c, action)
	}
}

func TestOperationListDoesNotGrantEmptyPermission(t *testing.T) {
	c := &protocol.Class{Operations: []string{protocol.OP_SELECT}}
	p := &protocol.Permission{}
	if operationAllowed(c, p, protocol.OP_SELECT) {
		t.Fatal("empty select must deny")
	}
	if operationAllowed(c, p, protocol.OP_UPDATE) {
		t.Fatal("closed operation must deny")
	}
}
