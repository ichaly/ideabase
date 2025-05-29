// Package pgsql 实现PostgreSQL的SQL方言
package pgsql

import (
	"fmt"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/vektah/gqlparser/v2/ast"
)

// buildUpdate 构建UPDATE语句
func (my *Dialect) buildUpdate(ctx *compiler.Context, field *ast.Field) error {
	// 验证表名
	if field.Name == "" {
		return fmt.Errorf("table name is required")
	}

	// 获取更新参数
	update := field.Arguments.ForName("update")
	if update == nil {
		return fmt.Errorf("update argument is required")
	}

	// 开始构建UPDATE语句
	ctx.SpaceAfter("UPDATE").
		Quote(field.Name).
		SpaceAfter("SET")

	// 处理更新字段
	if update.Value == nil || len(update.Value.Children) == 0 {
		return fmt.Errorf("update fields are required")
	}

	for i, child := range update.Value.Children {
		if i > 0 {
			ctx.Write(",")
		}
		if child.Name == "" {
			return fmt.Errorf("empty field name in update at index %d", i)
		}
		ctx.SpaceAfter("").
			Quote(child.Name).
			Write(" = ")

		// 添加参数占位符
		value, err := child.Value.Value(nil)
		if err != nil {
			return fmt.Errorf("failed to get value for field %s: %w", child.Name, err)
		}
		ctx.Write(my.Placeholder(len(ctx.Args()) + 1))
		ctx.AddParam(value)
	}

	// 处理WHERE条件
	if err := my.buildWhere(ctx, field.Arguments); err != nil {
		return fmt.Errorf("failed to build WHERE clause: %w", err)
	}

	// 添加RETURNING子句
	if len(field.SelectionSet) > 0 {
		ctx.SpaceAfter("RETURNING")
		for i, selection := range field.SelectionSet {
			f, ok := selection.(*ast.Field)
			if !ok {
				return fmt.Errorf("invalid selection type at index %d", i)
			}
			if f.Name == "" {
				return fmt.Errorf("empty field name in RETURNING clause at index %d", i)
			}
			if i > 0 {
				ctx.Write(", ")
			}
			ctx.Quote(f.Name)
		}
	}

	return nil
}
