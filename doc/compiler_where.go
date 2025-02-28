package gql

import (
	"github.com/duke-git/lancet/v2/convertor"
	"github.com/vektah/gqlparser/v2/ast"
	"strings"
)

func (my *compilerContext) buildChildValue(key, operator, raw string, kind ast.ValueKind) *ast.ChildValue {
	return &ast.ChildValue{Value: &ast.Value{Kind: ast.ObjectValue, Children: []*ast.ChildValue{{
		Name: key, Value: &ast.Value{Kind: ast.ObjectValue, Children: []*ast.ChildValue{
			{Name: operator, Value: &ast.Value{Kind: kind, Raw: raw}},
		}}},
	}}}
}

func (my *compilerContext) appendWhereValue(field *ast.Field, value *ast.Value) {
	where := field.Arguments.ForName(WHERE)
	//拼接原始条件
	if where == nil {
		where = &ast.Argument{Name: WHERE, Value: value}
		field.Arguments = append(field.Arguments, where)
	} else {
		//使用AND包装拼接关联关系查询条件
		where.Value = &ast.Value{Kind: ast.ObjectValue, Children: []*ast.ChildValue{
			{Name: AND, Value: &ast.Value{Kind: ast.ListValue, Children: []*ast.ChildValue{
				{Value: where.Value},
				{Value: value},
			}}},
		}}
	}
}

func (my *compilerContext) renderWhereField(field *ast.Field) {
	where := field.Arguments.ForName(WHERE)
	if where != nil {
		my.Write(` WHERE (`)
		my.renderWhereValue(where.Value)
		my.Write(`)`)
	}
}

func (my *compilerContext) renderWhereValue(value *ast.Value) {
	if value == nil {
		return
	}
	if value.Raw != "" {
		if value.Kind == ast.EnumValue {
			my.Write(strings.ReplaceAll(convertor.ToString(value.Raw), "_", " "))
		} else if value.Kind == ast.BlockValue {
			my.Write(value.Raw)
		} else {
			my.renderParam(value)
		}
		return
	}
	for _, v := range value.Children {
		switch v.Name {
		case IS, IN, EQ, NE, GT, GE, LT, LE, LIKE, I_LIKE, REGEX, I_REGEX:
			if s, ok := dictionary[v.Name]; ok {
				my.Write(" ")
				my.Write(strings.ToUpper(s.Text))
				my.Write(" ")
			}
			my.renderWhereValue(v.Value)
		case OR, AND:
			my.Write("(")
			for i, child := range v.Value.Children {
				if i > 0 {
					my.Write(" ")
					my.Write(strings.ToUpper(v.Name))
					my.Write(" ")
				}
				my.renderWhereValue(child.Value)
			}
			my.Write(")")
		case NOT:
			my.Write("NOT (")
			my.renderWhereValue(value.Children[0].Value)
			my.Write(")")
		default:
			my.Write("(")
			// TODO：更合适的办法？如果Definition为空，则认为是多表关联条件使用字段名称
			if value.Definition != nil {
				name := strings.TrimSuffix(value.Definition.Name, SUFFIX_WHERE_INPUT)
				table, _ := my.meta.TableName(name, false)
				column, _ := my.meta.ColumnName(name, v.Name, false)
				my.Quoted(table)
				my.Write(".")
				my.Quoted(column)
			} else if v.Name != "" {
				my.Write("(")
				my.Write(v.Name)
				my.Write(")")
			}
			my.renderWhereValue(v.Value)
			my.Write(")")
		}
	}
}
