// Package pgsql 实现PostgreSQL的SQL方言
package pgsql

import (
	"fmt"

	"github.com/ichaly/ideabase/gql"
	"github.com/vektah/gqlparser/v2/ast"
)

// init 在包初始化时自动注册PostgreSQL方言
func init() {
	// 注册PostgreSQL方言
	gql.RegisterDialect("postgresql", &Dialect{})
}

// Dialect PostgreSQL方言实现
type Dialect struct{}

// NewDialect 创建PostgreSQL方言实例
func NewDialect() gql.Dialect {
	return &Dialect{}
}

// Placeholder 获取参数占位符 (PostgreSQL使用$1,$2...)
func (my *Dialect) Placeholder(index int) string {
	return fmt.Sprintf("$%d", index)
}

// FormatLimit 格式化LIMIT子句
func (my *Dialect) FormatLimit(limit, offset int) string {
	if limit <= 0 && offset <= 0 {
		return ""
	}

	var result string
	if limit > 0 {
		result = fmt.Sprintf("LIMIT %d", limit)
	}

	if offset > 0 {
		if len(result) > 0 {
			result += " "
		}
		result += fmt.Sprintf("OFFSET %d", offset)
	}

	return result
}

// BuildQuery 构建查询语句
func (my *Dialect) BuildQuery(cpl *gql.Compiler, selectionSet ast.SelectionSet) error {
	// 获取第一个字段作为主查询
	if len(selectionSet) == 0 {
		return fmt.Errorf("empty selection set")
	}

	field, ok := selectionSet[0].(*ast.Field)
	if !ok {
		return fmt.Errorf("first selection must be a field")
	}

	// 构建基础查询
	cpl.Write("SELECT ")

	// 处理字段选择
	if len(field.SelectionSet) == 0 {
		cpl.Write("*")
	} else {
		my.buildSelectFields(cpl, field.SelectionSet)
	}

	cpl.Space("FROM")
	cpl.Wrap(`"`, field.Name)

	// 处理分页
	if err := my.buildPagination(cpl, field.Arguments); err != nil {
		return err
	}

	return nil
}

// buildSelectFields 构建选择字段
func (my *Dialect) buildSelectFields(cpl *gql.Compiler, selectionSet ast.SelectionSet) {
	for index, selection := range selectionSet {
		if field, ok := selection.(*ast.Field); ok {
			if index > 0 {
				cpl.Space(",")
			}
			cpl.Wrap(`"`, field.Name)
		}
	}
}

// buildWhere 构建WHERE子句
func (my *Dialect) buildWhere(cpl *gql.Compiler, args ast.ArgumentList) error {
	for _, arg := range args {
		if arg.Name == "where" {
			cpl.Write(" WHERE ")
			// TODO: 实现复杂的WHERE条件构建
			return nil
		}
	}
	return nil
}

// buildOrderBy 构建ORDER BY子句
func (my *Dialect) buildOrderBy(cpl *gql.Compiler, args ast.ArgumentList) error {
	for _, arg := range args {
		if arg.Name == "order_by" {
			cpl.Write(" ORDER BY ")
			// TODO: 实现排序构建
			return nil
		}
	}
	return nil
}

// buildPagination 构建分页
func (my *Dialect) buildPagination(cpl *gql.Compiler, args ast.ArgumentList) error {
	var limit, offset int

	for _, arg := range args {
		switch arg.Name {
		case "limit":
			// 获取limit参数值
			if val, err := arg.Value.Value(nil); err == nil {
				if intVal, ok := val.(int64); ok {
					limit = int(intVal)
				}
			}
		case "offset":
			// 获取offset参数值
			if val, err := arg.Value.Value(nil); err == nil {
				if intVal, ok := val.(int64); ok {
					offset = int(intVal)
				}
			}
		}
	}

	if limitClause := my.FormatLimit(limit, offset); limitClause != "" {
		cpl.Write(" " + limitClause)
	}
	return nil
}

// BuildMutation 构建变更语句
func (my *Dialect) BuildMutation(cpl *gql.Compiler, selectionSet ast.SelectionSet) error {
	if len(selectionSet) == 0 {
		return fmt.Errorf("empty selection set")
	}

	field, ok := selectionSet[0].(*ast.Field)
	if !ok {
		return fmt.Errorf("first selection must be a field")
	}

	op := field.Definition.Name // 操作类型: insert/update/delete

	switch op {
	case "insert":
		return my.buildInsert(cpl, field)
	case "update":
		return my.buildUpdate(cpl, field)
	case "delete":
		return my.buildDelete(cpl, field)
	default:
		return fmt.Errorf("unsupported mutation operation: %s", op)
	}
}

// buildInsert 构建INSERT语句
func (my *Dialect) buildInsert(cpl *gql.Compiler, field *ast.Field) error {
	// TODO: 实现INSERT语句构建
	return nil
}

// buildUpdate 构建UPDATE语句
func (my *Dialect) buildUpdate(cpl *gql.Compiler, field *ast.Field) error {
	// TODO: 实现UPDATE语句构建
	return nil
}

// buildDelete 构建DELETE语句
func (my *Dialect) buildDelete(cpl *gql.Compiler, field *ast.Field) error {
	// TODO: 实现DELETE语句构建
	return nil
}
