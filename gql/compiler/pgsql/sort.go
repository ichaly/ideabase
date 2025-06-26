// Package pgsql 排序处理模块
package pgsql

import (
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/vektah/gqlparser/v2/ast"
)

// buildOrderBy 构建ORDER BY子句 - 统一入口方法
func (my *Dialect) buildOrderBy(ctx *compiler.Context, args ast.ArgumentList) error {
	// 优先处理sort参数（新版本）
	if err := my.buildSortOrderBy(ctx, args); err != nil {
		return err
	}

	// 兼容处理orderBy参数（旧版本）
	if err := my.buildLegacyOrderBy(ctx, args); err != nil {
		return err
	}

	return nil
}

// buildSortOrderBy 构建基于sort参数的ORDER BY子句
func (my *Dialect) buildSortOrderBy(ctx *compiler.Context, args ast.ArgumentList) error {
	sortArg := args.ForName(gql.SORT)
	if sortArg == nil || sortArg.Value == nil {
		return nil
	}

	ctx.Space("ORDER BY")
	return my.buildSortValue(ctx, sortArg.Value)
}

// buildLegacyOrderBy 构建基于orderBy参数的ORDER BY子句（向后兼容）
func (my *Dialect) buildLegacyOrderBy(ctx *compiler.Context, args ast.ArgumentList) error {
	if len(args) == 0 {
		return nil
	}

	var hasOrderBy bool
	for _, arg := range args {
		if arg.Name != "orderBy" {
			continue
		}

		if arg.Value == nil || len(arg.Value.Children) == 0 {
			continue
		}

		if !hasOrderBy {
			ctx.Space("ORDER BY")
			hasOrderBy = true
		}

		for i, child := range arg.Value.Children {
			if i > 0 {
				ctx.Write(",")
			}

			if child.Name == "" {
				return fmt.Errorf("empty field name in ORDER BY at index %d", i)
			}

			ctx.Space("").Quote(child.Name)

			value, err := child.Value.Value(nil)
			if err != nil {
				return fmt.Errorf("failed to get value for order by field %s: %w", child.Name, err)
			}

			direction, ok := value.(string)
			if !ok {
				return fmt.Errorf("order by value must be a string, got %T", value)
			}

			switch strings.ToUpper(direction) {
			case "ASC", "DESC":
				ctx.Space(strings.ToUpper(direction))
			default:
				return fmt.Errorf("invalid order by direction %q, must be ASC or DESC", direction)
			}
		}
	}

	return nil
}

// buildSortValue 构建排序值
func (my *Dialect) buildSortValue(ctx *compiler.Context, value *ast.Value) error {
	if value == nil || len(value.Children) == 0 {
		return nil
	}

	// 处理排序字段列表
	for i, child := range value.Children {
		if i > 0 {
			ctx.Write(", ")
		}
		if err := my.buildSortField(ctx, child); err != nil {
			return err
		}
	}

	return nil
}

// buildSortField 构建单个排序字段
func (my *Dialect) buildSortField(ctx *compiler.Context, child *ast.ChildValue) error {
	if child == nil || child.Name == "" {
		return fmt.Errorf("invalid sort field: empty name")
	}

	// 构建字段名
	ctx.Quote(child.Name)

	// 处理排序方向
	if child.Value != nil && child.Value.Raw != "" {
		direction := strings.ToUpper(child.Value.Raw)
		// 处理PostgreSQL特有的NULL值排序
		switch direction {
		case "ASC_NULLS_FIRST":
			ctx.Write(" ASC NULLS FIRST")
		case "DESC_NULLS_FIRST":
			ctx.Write(" DESC NULLS FIRST")
		case "ASC_NULLS_LAST":
			ctx.Write(" ASC NULLS LAST")
		case "DESC_NULLS_LAST":
			ctx.Write(" DESC NULLS LAST")
		case "ASC":
			ctx.Write(" ASC")
		case "DESC":
			ctx.Write(" DESC")
		default:
			ctx.Write(" ASC") // 默认升序
		}
	} else {
		ctx.Write(" ASC") // 默认升序
	}

	return nil
}

// buildSortFieldWithAlias 构建带表别名的排序字段
func (my *Dialect) buildSortFieldWithAlias(ctx *compiler.Context, child *ast.ChildValue, alias string) error {
	if child == nil || child.Name == "" {
		return fmt.Errorf("invalid sort field: empty name")
	}

	// 构建带别名的字段名
	if alias != "" {
		ctx.Quote(alias).Write(".").Quote(child.Name)
	} else {
		ctx.Quote(child.Name)
	}

	// 处理排序方向（复用现有逻辑）
	if child.Value != nil && child.Value.Raw != "" {
		direction := strings.ToUpper(child.Value.Raw)
		switch direction {
		case "ASC_NULLS_FIRST":
			ctx.Write(" ASC NULLS FIRST")
		case "DESC_NULLS_FIRST":
			ctx.Write(" DESC NULLS FIRST")
		case "ASC_NULLS_LAST":
			ctx.Write(" ASC NULLS LAST")
		case "DESC_NULLS_LAST":
			ctx.Write(" DESC NULLS LAST")
		case "ASC":
			ctx.Write(" ASC")
		case "DESC":
			ctx.Write(" DESC")
		default:
			ctx.Write(" ASC")
		}
	} else {
		ctx.Write(" ASC")
	}

	return nil
}

// buildOrderByWithAlias 构建带表别名的ORDER BY子句
func (my *Dialect) buildOrderByWithAlias(ctx *compiler.Context, args ast.ArgumentList, alias string) error {
	sortArg := args.ForName(gql.SORT)
	if sortArg == nil || sortArg.Value == nil {
		return nil
	}

	ctx.Space("ORDER BY")
	return my.buildSortValueWithAlias(ctx, sortArg.Value, alias)
}

// buildSortValueWithAlias 构建带表别名的排序值
func (my *Dialect) buildSortValueWithAlias(ctx *compiler.Context, value *ast.Value, alias string) error {
	if value == nil || len(value.Children) == 0 {
		return nil
	}

	// 处理排序字段列表
	for i, child := range value.Children {
		if i > 0 {
			ctx.Write(", ")
		}
		if err := my.buildSortFieldWithAlias(ctx, child, alias); err != nil {
			return err
		}
	}

	return nil
}
