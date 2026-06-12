// Package pgsql 排序处理模块
package pgsql

import (
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2/ast"
)

// directions 排序方向枚举到SQL的映射
var directions = map[string]string{
	"ASC":              "ASC",
	"DESC":             "DESC",
	"ASC_NULLS_FIRST":  "ASC NULLS FIRST",
	"ASC_NULLS_LAST":   "ASC NULLS LAST",
	"DESC_NULLS_FIRST": "DESC NULLS FIRST",
	"DESC_NULLS_LAST":  "DESC NULLS LAST",
}

// buildOrderBy 构建ORDER BY子句，列名经scope映射并限定
func (my *Dialect) buildOrderBy(ctx *compiler.Context, sc scope, args ast.ArgumentList) error {
	entries := sortEntries(args)
	if len(entries) == 0 {
		return nil
	}

	ctx.Space("ORDER BY")
	for i, child := range entries {
		if i > 0 {
			ctx.Write(", ")
		}
		if child.Name == "" {
			return fmt.Errorf("排序字段名为空")
		}

		if sc.qualifier != "" {
			ctx.Quote(sc.qualifier).Write(".")
		}
		ctx.Quote(sc.column(child.Name))

		direction := "ASC"
		if child.Value != nil && child.Value.Raw != "" {
			var ok bool
			if direction, ok = directions[strings.ToUpper(child.Value.Raw)]; !ok {
				return fmt.Errorf("无效的排序方向: %s", child.Value.Raw)
			}
		}
		ctx.SpaceBefore(direction)
	}
	return nil
}

// sortEntries 提取排序键值对，sort参数兼容单对象与对象列表两种写法
func sortEntries(args ast.ArgumentList) []*ast.ChildValue {
	sortArg := args.ForName(protocol.SORT)
	if sortArg == nil || sortArg.Value == nil {
		return nil
	}

	var entries []*ast.ChildValue
	for _, child := range sortArg.Value.Children {
		if child.Name == "" && child.Value != nil { // 列表项：展开对象成员
			entries = append(entries, child.Value.Children...)
		} else {
			entries = append(entries, child)
		}
	}
	return entries
}

// sortColumns 返回排序涉及的列名，用于基础查询的列收集
func sortColumns(sc scope, args ast.ArgumentList) []string {
	entries := sortEntries(args)
	columns := make([]string, 0, len(entries))
	for _, child := range entries {
		columns = append(columns, sc.column(child.Name))
	}
	return columns
}
