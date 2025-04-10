package gql

import (
	"context"

	"github.com/vektah/gqlparser/v2/ast"
)

// Resolver GraphQL解析器
type Resolver struct {
	executor *Executor
	schema   *ast.Schema
}

// NewResolver 创建一个新的解析器
func NewResolver(e *Executor, s *ast.Schema) *Resolver {
	return &Resolver{executor: e, schema: s}
}

// Resolve 解析GraphQL查询
func (my *Resolver) Resolve(ctx context.Context, query string, variables map[string]interface{}, operationName string) (map[string]interface{}, error) {
	// 直接使用map类型变量执行查询
	result := my.executor.Execute(ctx, query, variables, operationName)

	// 处理错误
	if len(result.Errors) > 0 {
		return result.Data, result.Errors
	}

	return result.Data, nil
}
