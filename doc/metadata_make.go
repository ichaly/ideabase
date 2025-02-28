package gql

import (
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/samber/lo"
	"github.com/vektah/gqlparser/v2/ast"
)

func (my *Metadata) entryOption() error {
	//构建入口节点
	query := &internal.Class{
		Virtual: true,
		Name:    lo.Capitalize(string(ast.Query)),
		Fields:  make(map[string]*internal.Field),
	}
	mutation := &internal.Class{
		Virtual: true,
		Name:    lo.Capitalize(string(ast.Mutation)),
		Fields:  make(map[string]*internal.Field),
	}
	for k, v := range my.Nodes {
		if v.Kind != ast.Object {
			continue
		}
		_, name := my.Named(query.Name, k, JoinListSuffix())
		query.Fields[name] = &internal.Field{
			Name:      name,
			Type:      ast.ListType(ast.NamedType(v.Name, nil), nil),
			Virtual:   query.Virtual,
			Arguments: inputs(k),
		}
		mutation.Fields[name] = &internal.Field{
			Name:      name,
			Type:      ast.ListType(ast.NamedType(v.Name, nil), nil),
			Virtual:   mutation.Virtual,
			Arguments: inputs(k, ast.Mutation),
		}
	}
	my.Nodes[query.Name] = query
	my.Nodes[mutation.Name] = mutation
	return nil
}
