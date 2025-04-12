// Package pgsql 实现PostgreSQL的SQL方言
package pgsql

import (
	"fmt"

	"github.com/ichaly/ideabase/gql"
	"github.com/vektah/gqlparser/v2/ast"
)

// BuildQuery 构建查询语句
func (my *Dialect) BuildQuery(cpl *gql.Compiler, set ast.SelectionSet) error {
	if len(set) == 0 {
		return fmt.Errorf("empty selection set")
	}

	// 统一使用WITH语句
	cpl.SpaceAfter("WITH")

	// 构建每个查询的CTE
	for i, selection := range set {
		field, ok := selection.(*ast.Field)
		if !ok {
			return fmt.Errorf("selection must be a field")
		}

		if i > 0 {
			cpl.SpaceAfter(",")
		}

		// 创建CTE
		cpl.Write(field.Name).Space("AS").Write("(")

		if err := my.buildSingleQuery(cpl, field); err != nil {
			return err
		}
		cpl.Write(")")
	}

	// 构建最终的结果集
	cpl.SpaceAfter("SELECT")
	if len(set) > 1 {
		// 多表查询返回JSON对象
		cpl.Write("json_build_object(")
		for i, selection := range set {
			field := selection.(*ast.Field)
			if i > 0 {
				cpl.SpaceAfter(",")
			}
			cpl.Quote(field.Name).
				Write(", (SELECT row_to_json(").
				Write(field.Name).
				Write(".*) FROM ").
				Write(field.Name).
				Write(")")
		}
		cpl.Write(")")
	} else {
		// 单表查询直接返回结果
		field := set[0].(*ast.Field)
		cpl.Write("row_to_json(").
			Write(field.Name).
			Write(".*) FROM ").
			Write(field.Name)
	}

	return nil
}

// buildSingleQuery 构建单个表的查询
func (my *Dialect) buildSingleQuery(cpl *gql.Compiler, field *ast.Field) error {
	if field.Name == "" {
		return fmt.Errorf("table name is required")
	}

	cpl.SpaceAfter("SELECT")

	// 处理字段选择
	if len(field.SelectionSet) == 0 {
		cpl.Write("*")
	} else {
		for i, selection := range field.SelectionSet {
			f, ok := selection.(*ast.Field)
			if !ok {
				return fmt.Errorf("invalid selection type at index %d", i)
			}
			if f.Name == "" {
				return fmt.Errorf("empty field name in selection at index %d", i)
			}
			if i > 0 {
				cpl.Write(", ")
			}
			cpl.Quote(f.Name)
		}
	}

	cpl.SpaceAfter("FROM").Quote(field.Name)

	// 处理WHERE条件
	if err := my.buildWhere(cpl, field.Arguments); err != nil {
		return fmt.Errorf("failed to build WHERE clause: %w", err)
	}

	// 处理排序
	if err := my.buildOrderBy(cpl, field.Arguments); err != nil {
		return fmt.Errorf("failed to build ORDER BY clause: %w", err)
	}

	// 处理分页
	if err := my.buildPagination(cpl, field.Arguments); err != nil {
		return fmt.Errorf("failed to build pagination: %w", err)
	}

	return nil
}
