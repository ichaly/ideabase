// Package mysql 实现MySQL的SQL方言
package mysql

import (
	"fmt"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2/ast"
)

// Dialect MySQL方言实现
type Dialect struct{}

// NewDialect 创建MySQL方言实例
func NewDialect() protocol.Dialect {
	return &Dialect{}
}

// Name 方言名称
func (my *Dialect) Name() string {
	return "mysql"
}

// Quotation 引号标识符
func (my *Dialect) Quotation() string {
	return "`"
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
func (my *Dialect) BuildQuery(ctx *compiler.Context, set ast.SelectionSet) error {
	ctx.Write("SELECT * FROM ")
	return nil
}

// BuildMutation 构建变更语句
func (my *Dialect) BuildMutation(ctx *compiler.Context, set ast.SelectionSet) error {
	ctx.Write("-- MySQL mutation placeholder")
	return nil
}
