// Package pgsql 实现PostgreSQL的SQL方言
package pgsql

import (
	"fmt"

	"github.com/ichaly/ideabase/gql"
	"github.com/vektah/gqlparser/v2/ast"
)

// buildUpdate 构建UPDATE语句
func (my *Dialect) buildUpdate(cpl *gql.Compiler, field *ast.Field) error {
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
	cpl.SpaceAfter("UPDATE").
		Quoted(field.Name).
		SpaceAfter("SET")

	// 处理更新字段
	if update.Value == nil || len(update.Value.Children) == 0 {
		return fmt.Errorf("update fields are required")
	}

	for i, child := range update.Value.Children {
		if i > 0 {
			cpl.Write(",")
		}
		if child.Name == "" {
			return fmt.Errorf("empty field name in update at index %d", i)
		}
		cpl.SpaceAfter("").
			Quoted(child.Name).
			Write(" = ")

		// 添加参数占位符
		value, err := child.Value.Value(nil)
		if err != nil {
			return fmt.Errorf("failed to get value for field %s: %w", child.Name, err)
		}
		cpl.Write(my.Placeholder(len(cpl.Args()) + 1))
		cpl.AddParam(value)
	}

	// 处理WHERE条件
	if err := my.buildWhere(cpl, field.Arguments); err != nil {
		return fmt.Errorf("failed to build WHERE clause: %w", err)
	}

	// 添加RETURNING子句
	if len(field.SelectionSet) > 0 {
		cpl.SpaceAfter("RETURNING")
		for i, selection := range field.SelectionSet {
			f, ok := selection.(*ast.Field)
			if !ok {
				return fmt.Errorf("invalid selection type at index %d", i)
			}
			if f.Name == "" {
				return fmt.Errorf("empty field name in RETURNING clause at index %d", i)
			}
			if i > 0 {
				cpl.Write(", ")
			}
			cpl.Quoted(f.Name)
		}
	}

	return nil
}
