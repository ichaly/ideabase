// Package pgsql 实现PostgreSQL的SQL方言
package pgsql

import (
	"fmt"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/vektah/gqlparser/v2/ast"
)

// Dialect PostgreSQL方言实现
type Dialect struct{}

// NewDialect 创建PostgreSQL方言实例
func NewDialect() compiler.Dialect {
	return &Dialect{}
}

// Name 方言名称
func (my *Dialect) Name() string {
	return "postgresql"
}

// Quotation 引号标识符
func (my *Dialect) Quotation() string {
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

// BuildMutation 构建变更语句
func (my *Dialect) BuildMutation(ctx *compiler.Context, set ast.SelectionSet) error {
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
		return my.buildInsert(ctx, field)
	case "update":
		return my.buildUpdate(ctx, field)
	case "delete":
		return my.buildDelete(ctx, field)
	default:
		return fmt.Errorf("unsupported mutation operation: %s", op)
	}
}

// buildPagination 构建分页子句
func (my *Dialect) buildPagination(ctx *compiler.Context, args ast.ArgumentList) error {
	// 处理排序
	if err := my.buildOrderBy(ctx, args); err != nil {
		return fmt.Errorf("failed to build order by: %w", err)
	}

	var (
		limit  int
		offset int
		after  interface{}
		before interface{}
	)

	// 处理分页参数
	for _, arg := range args {
		val, err := arg.Value.Value(nil)
		if err != nil {
			return fmt.Errorf("failed to get value for pagination argument %s: %w", arg.Name, err)
		}

		switch arg.Name {
		case "limit":
			if intVal, ok := val.(int64); ok {
				if intVal < 0 {
					return fmt.Errorf("limit must be non-negative, got %d", intVal)
				}
				limit = int(intVal)
			} else {
				return fmt.Errorf("limit must be an integer, got %T", val)
			}
		case "offset":
			if intVal, ok := val.(int64); ok {
				if intVal < 0 {
					return fmt.Errorf("offset must be non-negative, got %d", intVal)
				}
				offset = int(intVal)
			} else {
				return fmt.Errorf("offset must be an integer, got %T", val)
			}
		case "after":
			after = val
		case "before":
			before = val
		}
	}

	// 处理游标分页
	if after != nil || before != nil {
		if offset > 0 {
			return fmt.Errorf("cannot use offset with cursor-based pagination")
		}

		if after != nil {
			ctx.Space("AND id >").Write(my.Placeholder(len(ctx.Args()) + 1))
			ctx.AddParam(after)
		}

		if before != nil {
			ctx.Space("AND id <").Write(my.Placeholder(len(ctx.Args()) + 1))
			ctx.AddParam(before)
		}
	}

	// 添加LIMIT/OFFSET子句
	if limitClause := my.FormatLimit(limit, offset); limitClause != "" {
		ctx.Space(limitClause)
	}

	return nil
}
