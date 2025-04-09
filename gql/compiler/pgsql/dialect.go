package pgsql

import (
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/gql"
	"github.com/vektah/gqlparser/v2/ast"
)

// Dialect PostgreSQL方言实现
type Dialect struct{}

// QuoteIdentifier 为标识符添加引号(PostgreSQL使用双引号)
func (my *Dialect) QuoteIdentifier(identifier string) string {
	return fmt.Sprintf(`"%s"`, strings.ReplaceAll(identifier, `"`, `""`))
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

	if offset > 0 {
		return fmt.Sprintf("LIMIT %d OFFSET %d", limit, offset)
	}

	return fmt.Sprintf("LIMIT %d", limit)
}

// BuildQuery 构建查询语句
func (my *Dialect) BuildQuery(ctx *gql.Context, selectionSet ast.SelectionSet) error {
	// TODO: 实现PostgreSQL查询构建逻辑
	// 这里只是示例实现，实际逻辑需要根据具体需求开发
	ctx.Write("SELECT * FROM ")
	// 进一步实现...

	return nil
}

// BuildMutation 构建变更语句
func (my *Dialect) BuildMutation(ctx *gql.Context, selectionSet ast.SelectionSet) error {
	// TODO: 实现PostgreSQL变更构建逻辑
	// 这里只是示例实现，实际逻辑需要根据具体需求开发
	ctx.Write("-- PostgreSQL mutation placeholder")
	// 进一步实现...

	return nil
}

// SupportsReturning 是否支持RETURNING子句
func (my *Dialect) SupportsReturning() bool {
	return true
}

// SupportsWithCTE 是否支持WITH CTE
func (my *Dialect) SupportsWithCTE() bool {
	return true
}

// New 创建PostgreSQL方言实例
func New() gql.Dialect {
	return &Dialect{}
}

// NewDialect 创建PostgreSQL方言实例（导出函数）
func NewDialect() gql.Dialect {
	return &Dialect{}
}
