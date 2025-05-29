package gql

import (
	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/vektah/gqlparser/v2/ast"
)

// Compiler 编译上下文
type Compiler struct {
	meta    *Metadata        // 元数据引用
	dialect compiler.Dialect // 方言实现引用，避免重复查询
}

// NewCompiler 创建新的编译上下文
func NewCompiler(m *Metadata, dialects []compiler.Dialect) *Compiler {
	return &Compiler{
		meta:    m,
		dialect: dialects[0],
	}
}

func (my *Compiler) Build(operation *ast.OperationDefinition, variables map[string]interface{}) (string, []any, error) {
	ctx := compiler.NewContext(my.dialect, variables)
	switch operation.Operation {
	case ast.Query, ast.Subscription:
		my.dialect.BuildQuery(ctx, operation.SelectionSet)
	case ast.Mutation:
		my.dialect.BuildMutation(ctx, operation.SelectionSet)
	}
	return ctx.String(), ctx.Args(), nil
}
