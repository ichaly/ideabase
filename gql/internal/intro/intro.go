package intro

import (
	"github.com/vektah/gqlparser/v2/ast"
)

// New 创建一个新的内省支持
func New(schema *ast.Schema) interface{} {
	return &introspection{schema: schema}
}

// introspection GraphQL内省实现
type introspection struct {
	schema *ast.Schema
}

// 这里会实现GraphQL内省的相关方法
// 暂时只提供基本结构，后续可进一步实现
