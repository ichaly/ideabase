package mysql

import (
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/gql"
	"github.com/vektah/gqlparser/v2/ast"
)

// Dialect MySQL方言实现
type Dialect struct{}

// QuoteIdentifier 为标识符添加引号(MySQL使用反引号)
func (my *Dialect) QuoteIdentifier(identifier string) string {
	return fmt.Sprintf("`%s`", strings.ReplaceAll(identifier, "`", "``"))
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
func (my *Dialect) BuildQuery(ctx *gql.Context, selectionSet ast.SelectionSet) error {
	// TODO: 实现MySQL查询构建逻辑
	// 这里只是示例实现，实际逻辑需要根据具体需求开发
	ctx.Write("SELECT * FROM ")
	// 进一步实现...

	return nil
}

// BuildMutation 构建变更语句
func (my *Dialect) BuildMutation(ctx *gql.Context, selectionSet ast.SelectionSet) error {
	// TODO: 实现MySQL变更构建逻辑
	// 这里只是示例实现，实际逻辑需要根据具体需求开发
	ctx.Write("-- MySQL mutation placeholder")
	// 进一步实现...

	return nil
}

// SupportsReturning 是否支持RETURNING子句(MySQL 8.0.21+支持)
func (my *Dialect) SupportsReturning() bool {
	// 注意：根据MySQL版本可能需要调整
	// MySQL 8.0.21及以上版本支持RETURNING
	return false
}

// SupportsWithCTE 是否支持WITH CTE
func (my *Dialect) SupportsWithCTE() bool {
	// MySQL 8.0+支持CTE
	return true
}

// New 创建MySQL方言实例
func New() gql.Dialect {
	return &Dialect{}
}

// NewDialect 创建MySQL方言实例（导出函数）
func NewDialect() gql.Dialect {
	return &Dialect{}
}
