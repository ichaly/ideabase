// Package mysql 实现MySQL的SQL方言
package mysql

import (
	"fmt"

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

// NewDialect 创建MySQL方言实例
func NewDialect() gql.Dialect {
	return &Dialect{}
}

// Placeholder 获取参数占位符 (MySQL使用?)
func (my *Dialect) Placeholder(index int) string {
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
