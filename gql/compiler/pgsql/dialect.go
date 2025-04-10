// Package pgsql 实现PostgreSQL的SQL方言
package pgsql

import (
	"fmt"
	"strings"

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

// QuoteIdentifier 为标识符添加引号
func (my *Dialect) QuoteIdentifier(identifier string) string {
	parts := strings.Split(identifier, ".")
	for i, part := range parts {
		if part != "*" {
			parts[i] = "\"" + part + "\""
		}
	}
	return strings.Join(parts, ".")
}

// ParamPlaceholder 获取参数占位符 (PostgreSQL使用$1,$2...)
func (my *Dialect) ParamPlaceholder(index int) string {
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
	cpl.Write("SELECT * FROM ")
	return nil
}

// BuildMutation 构建变更语句
func (my *Dialect) BuildMutation(cpl *gql.Compiler, selectionSet ast.SelectionSet) error {
	cpl.Write("-- PostgreSQL mutation placeholder")
	return nil
}

// SupportsReturning 是否支持RETURNING子句
func (my *Dialect) SupportsReturning() bool {
	// PostgreSQL支持RETURNING
	return true
}

// SupportsWithCTE 是否支持WITH CTE
func (my *Dialect) SupportsWithCTE() bool {
	// PostgreSQL支持WITH CTE
	return true
}

// NewDialect 创建PostgreSQL方言实例
func NewDialect() gql.Dialect {
	return &Dialect{}
}
