package protocol

import (
	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/vektah/gqlparser/v2/ast"
)

// Dialect 定义SQL方言接口（本包内定义，便于Context直接引用）
type Dialect interface {
	// Name 方言名称
	Name() string

	// Quotation 引号标识符
	Quotation() string

	// Placeholder 获取参数占位符 (如: PostgreSQL的$1,$2..., MySQL的?)
	Placeholder(index int) string

	// BuildQuery 构建查询语句
	BuildQuery(ctx *compiler.Context, set ast.SelectionSet) error

	// BuildMutation 构建变更语句
	BuildMutation(ctx *compiler.Context, set ast.SelectionSet) error
}
