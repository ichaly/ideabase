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

// BuildMutation 构建变更语句
func (my *Dialect) BuildMutation(ctx *compiler.Context, set ast.SelectionSet) error {
	return fmt.Errorf("mutation编译尚未实现")
}
