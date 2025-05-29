package pgsql

import (
	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/vektah/gqlparser/v2/ast"
)

// buildInsert 构建INSERT语句
func (my *Dialect) buildInsert(ctx *compiler.Context, field *ast.Field) error {
	ctx.SpaceAfter("INSERT INTO").
		Quote(field.Name).
		SpaceBefore("(")

	// 构建字段列表
	if err := my.buildInsertColumns(ctx, field); err != nil {
		return err
	}

	ctx.Write(")").SpaceAfter("VALUES").Write("(")

	// 构建值列表
	if err := my.buildInsertValues(ctx, field); err != nil {
		return err
	}

	ctx.Write(")")

	// 添加RETURNING子句
	if len(field.SelectionSet) > 0 {
		ctx.SpaceAfter("RETURNING")
		for i, selection := range field.SelectionSet {
			if f, ok := selection.(*ast.Field); ok {
				if i > 0 {
					ctx.Write(", ")
				}
				ctx.Quote(f.Name)
			}
		}
	}

	return nil
}

// buildInsertColumns 构建INSERT语句的字段列表
func (my *Dialect) buildInsertColumns(ctx *compiler.Context, field *ast.Field) error {
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
func (my *Dialect) buildInsertValues(ctx *compiler.Context, field *ast.Field) error {
	// 从input参数中获取值列表
	for _, arg := range field.Arguments {
		if arg.Name == "input" {
			// TODO: 从input对象中提取值
			return nil
		}
	}
	return nil
}
