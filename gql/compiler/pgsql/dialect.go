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

// QuoteIdentifier 为标识符添加引号
func (my *Dialect) QuoteIdentifier() string {
	return `"`
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
func (my *Dialect) BuildQuery(cpl *gql.Compiler, set ast.SelectionSet) error {
	if len(set) == 0 {
		return fmt.Errorf("empty selection set")
	}

	// 统一使用WITH语句
	cpl.Space("WITH", gql.After())

	// 构建每个查询的CTE
	for i, selection := range set {
		field, ok := selection.(*ast.Field)
		if !ok {
			return fmt.Errorf("selection must be a field")
		}

		if i > 0 {
			cpl.Write(", ")
		}

		// 创建CTE
		cpl.Write(field.Name).Space("AS").Write("(")

		if err := my.buildSingleQuery(cpl, field); err != nil {
			return err
		}
		cpl.Write(")")
	}

	// 构建最终的结果集
	cpl.Space("SELECT", gql.After())
	if len(set) > 1 {
		// 多表查询返回JSON对象
		cpl.Write("json_build_object(")
		for i, selection := range set {
			field := selection.(*ast.Field)
			if i > 0 {
				cpl.Write(", ")
			}
			cpl.Write("'").
				Write(field.Name).
				Write("', (SELECT row_to_json(").
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
	cpl.Space("SELECT", gql.After())

	// 处理字段选择
	if len(field.SelectionSet) == 0 {
		cpl.Write("*")
	} else {
		for i, selection := range field.SelectionSet {
			if f, ok := selection.(*ast.Field); ok {
				if i > 0 {
					cpl.Write(", ")
				}
				cpl.Quoted(f.Name)
			}
		}
	}

	cpl.Space("FROM", gql.After()).Quoted(field.Name)

	// 处理WHERE条件
	if err := my.buildWhere(cpl, field.Arguments); err != nil {
		return err
	}

	// 处理排序
	if err := my.buildOrderBy(cpl, field.Arguments); err != nil {
		return err
	}

	// 处理分页
	if err := my.buildPagination(cpl, field.Arguments); err != nil {
		return err
	}

	return nil
}

// buildWhere 构建WHERE子句
func (my *Dialect) buildWhere(cpl *gql.Compiler, args ast.ArgumentList) error {
	if len(args) == 0 {
		return nil
	}

	hasWhere := false
	for _, arg := range args {
		if hasWhere {
			cpl.Space("AND", gql.After())
		} else {
			cpl.Space("WHERE", gql.After())
			hasWhere = true
		}

		switch arg.Name {
		case "id":
			val, err := arg.Value.Value(nil)
			if err != nil {
				return err
			}
			cpl.Write("id").
				Space("=", gql.After()).
				Write(my.Placeholder(cpl.AddParam(val)))
		case "where":
			// TODO: 处理复杂的where条件
			return nil
		default:
			// 处理其他简单条件
			val, err := arg.Value.Value(nil)
			if err != nil {
				return err
			}
			cpl.Write(arg.Name).
				Space("=", gql.After()).
				Write(my.Placeholder(cpl.AddParam(val)))
		}
	}
	return nil
}

// buildOrderBy 构建ORDER BY子句
func (my *Dialect) buildOrderBy(cpl *gql.Compiler, args ast.ArgumentList) error {
	for _, arg := range args {
		if arg.Name == "order_by" {
			cpl.Space("ORDER BY", gql.After())
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
func (my *Dialect) BuildMutation(cpl *gql.Compiler, set ast.SelectionSet) error {
	if len(set) == 0 {
		return fmt.Errorf("empty selection set")
	}

	field, ok := set[0].(*ast.Field)
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
	cpl.Space("INSERT INTO", gql.After()).
		Quoted(field.Name).
		Space("(", gql.Before())

	// TODO: 实现字段和值的构建

	cpl.Write(")")
	return nil
}

// buildUpdate 构建UPDATE语句
func (my *Dialect) buildUpdate(cpl *gql.Compiler, field *ast.Field) error {
	cpl.Space("UPDATE", gql.After()).
		Quoted(field.Name).
		Space("SET", gql.After())

	// TODO: 实现SET和WHERE子句
	return nil
}

// buildDelete 构建DELETE语句
func (my *Dialect) buildDelete(cpl *gql.Compiler, field *ast.Field) error {
	cpl.Space("DELETE FROM", gql.After()).
		Quoted(field.Name)

	// TODO: 实现WHERE子句
	return nil
}
