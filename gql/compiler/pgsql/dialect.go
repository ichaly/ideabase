// Package pgsql 实现PostgreSQL的SQL方言
package pgsql

import (
	"strconv"

	"github.com/ichaly/ideabase/gql/compiler"
)

// 导入本包即自动注册PostgreSQL方言
func init() {
	compiler.Register(NewDialect())
}

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
	return "$" + strconv.Itoa(index)
}
