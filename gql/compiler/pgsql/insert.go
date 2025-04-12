package pgsql

import (
	"github.com/ichaly/ideabase/gql"
	"github.com/vektah/gqlparser/v2/ast"
)

// buildInsert 构建INSERT语句
func (my *Dialect) buildInsert(cpl *gql.Compiler, field *ast.Field) error {
	cpl.SpaceAfter("INSERT INTO").
		Quote(field.Name).
		SpaceBefore("(")

	// 构建字段列表
	if err := my.buildInsertColumns(cpl, field); err != nil {
		return err
	}

	cpl.Write(")").SpaceAfter("VALUES").Write("(")

	// 构建值列表
	if err := my.buildInsertValues(cpl, field); err != nil {
		return err
	}

	cpl.Write(")")

	// 添加RETURNING子句
	if len(field.SelectionSet) > 0 {
		cpl.SpaceAfter("RETURNING")
		for i, selection := range field.SelectionSet {
			if f, ok := selection.(*ast.Field); ok {
				if i > 0 {
					cpl.Write(", ")
				}
				cpl.Quote(f.Name)
			}
		}
	}

	return nil
}

// buildInsertColumns 构建INSERT语句的字段列表
func (my *Dialect) buildInsertColumns(cpl *gql.Compiler, field *ast.Field) error {
	// 从input参数中获取字段列表
	for _, arg := range field.Arguments {
		if arg.Name == "input" {
			// TODO: 从input对象中提取字段
			return nil
		}
	}
	return nil
}

// buildInsertValues 构建INSERT语句的值列表
func (my *Dialect) buildInsertValues(cpl *gql.Compiler, field *ast.Field) error {
	// 从input参数中获取值列表
	for _, arg := range field.Arguments {
		if arg.Name == "input" {
			// TODO: 从input对象中提取值
			return nil
		}
	}
	return nil
}
