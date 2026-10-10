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

func sortDirection(desc bool) string {
	if desc {
		return "DESC"
	}
	return "ASC"
}

// buildOrderBy 构建ORDER BY子句；entries由调用方解析（sortEntries），
// lead为前置列（DISTINCT ON要求排序以去重列开头）
func (my *Dialect) buildOrderBy(ctx *compiler.Context, sc scope, entries []*ast.ChildValue, lead, tail []string) error {
	if len(entries) == 0 && len(lead) == 0 && len(tail) == 0 {
		return nil
	}

	ctx.Space("ORDER BY")
	next := comma(ctx)
	for _, column := range lead {
		next()
		ctx.Column(sc.qualifier, column)
	}
	for _, child := range entries {
		next()
		if child.Name == "" {
			return fmt.Errorf("排序字段名为空")
		}
		ctx.Column(sc.qualifier, sc.column(child.Name))

		direction := "ASC"
		if child.Value != nil && child.Value.Raw != "" {
			var ok bool
			if direction, ok = directions[strings.ToUpper(child.Value.Raw)]; !ok {
				return fmt.Errorf("无效的排序方向: %s", child.Value.Raw)
			}
		}
		ctx.SpaceBefore(direction)
	}
	for _, name := range tail {
		if orderContains(name, lead, entries) {
			continue
		}
		next()
		ctx.Column(sc.qualifier, sc.column(name)).SpaceBefore("ASC")
	}
	return nil
}

func orderContains(name string, lead []string, entries []*ast.ChildValue) bool {
	for _, column := range lead {
		if column == name {
			return true
		}
	}
	for _, entry := range entries {
		if entry.Name == name {
			return true
		}
	}
	return false
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
func sortColumns(sc scope, entries []*ast.ChildValue) []string {
	columns := make([]string, 0, len(entries))
	for _, child := range entries {
		columns = append(columns, sc.column(child.Name))
	}
	return columns
}
