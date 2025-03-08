package gql

import (
	"context"

	"github.com/ichaly/ideabase/gql/internal/intro"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"gorm.io/gorm"
)

// 请求和结果类型定义
type (
	// gqlRequest GraphQL请求
	gqlRequest struct {
		Query         string
		OperationName string
		Variables     map[string]interface{}
	}

	// gqlResult GraphQL结果
	gqlResult struct {
		sql    string
		args   []any
		Data   map[string]interface{} `json:"data,omitempty"`
		Errors gqlerror.List          `json:"errors,omitempty"`
	}

	// gqlValue GraphQL值
	gqlValue struct {
		value interface{}
		name  string
		err   error
	}
)

// Executor GraphQL执行器
type Executor struct {
	db       *gorm.DB
	intro    interface{}
	schema   *ast.Schema
	compiler *Compiler
}

// NewExecutor 创建一个新的执行器
func NewExecutor(d *gorm.DB, s *ast.Schema, c *Compiler) (*Executor, error) {
	return &Executor{db: d, intro: intro.New(s), schema: s, compiler: c}, nil
}

// Execute 执行GraphQL查询
func (my *Executor) Execute(ctx context.Context, query string, variables RawMessage) (r gqlResult) {
	// 解析GraphQL查询
	doc, err := gqlparser.LoadQuery(my.schema, query)
	if err != nil {
		r.Errors = gqlerror.List{gqlerror.Wrap(err)}
		return
	}

	// 执行所有操作
	for _, operation := range doc.Operations {
		// 编译为SQL
		r.sql, r.args = my.compiler.Compile(operation, variables)

		// 执行SQL查询
		e := my.db.Raw(r.sql, r.args...).Scan(&r.Data).Error
		if e != nil {
			r.Errors = append(r.Errors, gqlerror.Wrap(e))
		}
	}

	return
}
