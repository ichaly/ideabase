package gql

import (
	"github.com/duke-git/lancet/v2/slice"
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/utl"
	"github.com/vektah/gqlparser/v2/ast"
)

func (my *Metadata) expressions() error {
	var build = func(scalar, suffix string, symbols []*internal.Symbol) {
		name := utl.JoinString(scalar, suffix)
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

func (my *Metadata) orderOption() error {
	for k, v := range my.Nodes {
		if v.Kind != ast.Object {
			continue
		}
		name := utl.JoinString(k, SUFFIX_SORT_INPUT)
		sort := &internal.Class{
			Name:   name,
			Kind:   ast.InputObject,
			Fields: make(map[string]*internal.Field),
		}
		for _, f := range v.Fields {
			if !slice.Contain(scalars, f.Type.Name()) {
				continue
			}
			sort.Fields[f.Name] = &internal.Field{
				Name: f.Name,
				Type: ast.NamedType(ENUM_SORT_INPUT, nil),
			}
		}
		my.Nodes[name] = sort
	}
	return nil
}

func (my *Metadata) whereOption() error {
	for k, v := range my.Nodes {
		if v.Kind != ast.Object {
			continue
		}
		name := utl.JoinString(k, SUFFIX_WHERE_INPUT)
		where := &internal.Class{
			Name: name,
			Kind: ast.InputObject,
			Fields: map[string]*internal.Field{
				NOT: {
					Name: NOT,
					Type: ast.NamedType(name, nil),
				},
				AND: {
					Name: AND,
					Type: ast.ListType(ast.NonNullNamedType(name, nil), nil),
				},
				OR: {
					Name: OR,
					Type: ast.ListType(ast.NonNullNamedType(name, nil), nil),
				},
			},
		}
		for _, f := range v.Fields {
			if !slice.Contain(scalars, f.Type.Name()) {
				continue
			}
			where.Fields[f.Name] = &internal.Field{
				Name: f.Name,
				Type: ast.NamedType(utl.JoinString(f.Type.Name(), SUFFIX_EXPRESSION), nil),
			}
		}
		my.Nodes[name] = where
	}
	return nil
}

func (my *Metadata) inputOption() error {
	list := []string{SUFFIX_UPDATE_INPUT, SUFFIX_UPSERT_INPUT, SUFFIX_INSERT_INPUT}
	for k, v := range my.Nodes {
		if v.Kind != ast.Object {
			continue
		}
		for _, suffix := range list {
			class := &internal.Class{
				Kind:   ast.InputObject,
				Name:   utl.JoinString(k, suffix),
				Fields: make(map[string]*internal.Field),
			}
			for _, f := range v.Fields {
				kind := ast.NamedType(f.Type.Name(), nil)
				if !slice.Contain(scalars, f.Type.Name()) {
					if suffix == SUFFIX_UPSERT_INPUT || f.Kind == MANY_TO_ONE || (f.Kind == RECURSIVE && f.Name == PARENTS) {
						continue
					}
					kind = ast.ListType(ast.NamedType(utl.JoinString(f.Type.Name(), suffix), nil), nil)
				}
				class.Fields[f.Name] = &internal.Field{Name: f.Name, Type: kind}
			}
			if suffix == SUFFIX_UPDATE_INPUT {
				name := utl.JoinString(k, SUFFIX_WHERE_INPUT)
				class.Fields[CONNECT] = &internal.Field{
					Name: CONNECT,
					Type: ast.NamedType(name, nil),
				}
				class.Fields[DISCONNECT] = &internal.Field{
					Name: DISCONNECT,
					Type: ast.NamedType(name, nil),
				}
			}
			my.Nodes[class.Name] = class
		}
	}
	return nil
}
