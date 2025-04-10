// Package mysql 实现MySQL的SQL方言
package mysql

import (
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/gql"
	"github.com/vektah/gqlparser/v2/ast"
)

// init 在包初始化时自动注册MySQL方言
func init() {
	// 注册MySQL方言
	gql.RegisterDialect("mysql", &Dialect{})
}

// Dialect MySQL方言实现
type Dialect struct{}

// QuoteIdentifier 为标识符添加引号
func (my *Dialect) QuoteIdentifier(identifier string) string {
	parts := strings.Split(identifier, ".")
	for i, part := range parts {
		if part != "*" {
			parts[i] = "`" + part + "`"
		}
	}
	return strings.Join(parts, ".")
}

// ParamPlaceholder 获取参数占位符 (MySQL使用?)
func (my *Dialect) ParamPlaceholder(index int) string {
	return "?"
}

// FormatLimit 格式化LIMIT子句
func (my *Dialect) FormatLimit(limit, offset int) string {
	if limit <= 0 && offset <= 0 {
		return ""
	}

	if offset > 0 {
		return fmt.Sprintf("LIMIT %d, %d", offset, limit)
	}

	return fmt.Sprintf("LIMIT %d", limit)
}

// BuildQuery 构建查询语句
func (my *Dialect) BuildQuery(cpl *gql.Compiler, selectionSet ast.SelectionSet) error {
	cpl.Write("SELECT * FROM ")
	return nil
}

// BuildMutation 构建变更语句
func (my *Dialect) BuildMutation(cpl *gql.Compiler, selectionSet ast.SelectionSet) error {
	cpl.Write("-- MySQL mutation placeholder")
	return nil
}

// SupportsReturning 是否支持RETURNING子句(MySQL 8.0.21+支持)
func (my *Dialect) SupportsReturning() bool {
	return false // MySQL 8.0.21以上才支持
}

// SupportsWithCTE 是否支持WITH CTE
func (my *Dialect) SupportsWithCTE() bool {
	return true // MySQL 8.0+支持CTE
}

// NewDialect 创建MySQL方言实例
func NewDialect() gql.Dialect {
	return &Dialect{}
}
