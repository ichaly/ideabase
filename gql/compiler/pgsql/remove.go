// Package pgsql 实现PostgreSQL的SQL方言
package pgsql

import (
	"fmt"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/vektah/gqlparser/v2/ast"
)

// buildDelete 构建DELETE语句
func (my *Dialect) buildDelete(ctx *compiler.Context, field *ast.Field) error {
	// 验证表名
	if field.Name == "" {
		return fmt.Errorf("table name is required")
	}

	// 开始构建DELETE语句
	ctx.SpaceAfter("DELETE FROM").Quote(field.Name)

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
