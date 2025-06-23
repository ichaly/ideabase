package gql

import (
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/vektah/gqlparser/v2/ast"
)

// Compiler 编译上下文
type Compiler struct {
	meta    *Metadata        // 元数据引用
	dialect compiler.Dialect // 方言实现引用，避免重复查询
}

// NewCompiler 创建新的编译上下文
func NewCompiler(m *Metadata, dialects []compiler.Dialect) (*Compiler, error) {
	my := &Compiler{meta: m}
	if err := my.selectDialect(dialects); err != nil {
		return nil, err
	}
	return my, nil
}

func (my *Compiler) Build(operation *ast.OperationDefinition, variables map[string]interface{}) (string, []any, error) {
	ctx := compiler.NewContext(my.meta, my.dialect.Quotation(), variables)
	switch operation.Operation {
	case ast.Query, ast.Subscription:
		my.dialect.BuildQuery(ctx, operation.SelectionSet)
	case ast.Mutation:
		my.dialect.BuildMutation(ctx, operation.SelectionSet)
	}
	return ctx.String(), ctx.Args(), nil
}

// selectDialect 选择适合当前数据库的SQL方言
// 方言选择逻辑:
// 1. 优先根据数据库驱动类型选择对应方言
// 2. 如未找到匹配，尝试使用PostgreSQL方言(推荐方言)
// 3. 如仍未找到，使用首个可用方言
// 4. 如无可用方言，返回错误
// 返回: 如无可用方言则返回错误
func (my *Compiler) selectDialect(list []compiler.Dialect) error {
	dialects := make(map[string]compiler.Dialect, len(list))
	for _, dialect := range list {
		dialects[dialect.Name()] = dialect
	}
	// 1. 首先尝试根据数据库类型选择方言
	if my.meta != nil && my.meta.db != nil {
		dbName := my.meta.db.Name()

		// 根据数据库驱动名称匹配方言
		switch {
		case strings.Contains(dbName, "postgres"):
			if dialect, ok := dialects["postgresql"]; ok {
				my.dialect = dialect
			}
		case strings.Contains(dbName, "mysql"):
			if dialect, ok := dialects["mysql"]; ok {
				my.dialect = dialect
			}
		}
	}

	// 2. 如果未找到匹配方言，尝试使用PostgreSQL方言（如果存在）
	if my.dialect == nil && len(dialects) > 0 {
		if dialect, ok := dialects["postgresql"]; ok {
			my.dialect = dialect
		} else {
			// 3. 否则使用第一个可用的方言
			for _, dialect := range dialects {
				my.dialect = dialect
				break
			}
		}
	}

	// 4. 如果仍未找到方言，返回错误
	if my.dialect == nil {
		return fmt.Errorf("没有可用的SQL方言实现")
	}

	return nil
}
