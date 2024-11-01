package graphql

import (
	"github.com/ichaly/ideabase/graphql/internal"
	"github.com/ichaly/ideabase/utility"
	"github.com/vektah/gqlparser/v2/ast"
)

func (my *Metadata) expressions() error {
	var build = func(scalar, suffix string, symbols []*internal.Symbol) {
		name := utility.JoinString(scalar, suffix)
		expr := &internal.Class{Name: name, Kind: ast.InputObject, Fields: make(map[string]*internal.Field)}
		for _, v := range symbols {
			var t *ast.Type
			if v.Name == IS {
				t = ast.NamedType(ENUM_IS_INPUT, nil)
			} else if v.Name == IN {
				t = ast.ListType(ast.NonNullNamedType(scalar, nil), nil)
			} else {
				t = ast.NamedType(scalar, nil)
			}
			expr.Fields[v.Name] = &internal.Field{Type: t, Name: v.Name, Description: v.Describe}
		}
		my.Nodes[name] = expr
	}
	for _, s := range scalars {
		build(s, SUFFIX_EXPRESSION, symbols[s])
		build(s, SUFFIX_EXPRESSION_LIST, symbols[s])
	}
	return nil
}
