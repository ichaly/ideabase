package gql

import (
	"context"
	"strings"

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
	intro    *intro.Handler
	schema   *ast.Schema
	compiler *Compiler
}

// NewExecutor 创建一个新的执行器
func NewExecutor(d *gorm.DB, s *ast.Schema, c *Compiler) (*Executor, error) {
	i := intro.New(s)
	return &Executor{
		db:       d,
		intro:    i,
		schema:   s,
		compiler: c,
	}, nil
}

// Execute 执行GraphQL查询
func (my *Executor) Execute(ctx context.Context, query string, variables RawMessage) (r gqlResult) {
	// 解析变量
	var vars map[string]interface{}
	if len(variables) > 0 {
		if err := json.Unmarshal(variables, &vars); err != nil {
			r.Errors = gqlerror.List{gqlerror.Wrap(err)}
			return
		}
	}

	// 检查是否是自省查询
	if isIntrospectionQuery(query) {
		// 处理自省查询
		data, err := my.intro.Introspect(ctx, query, vars)
		if err != nil {
			r.Errors = gqlerror.List{gqlerror.Wrap(err)}
			return
		}

		// 将结果转换为map
		dataJson, err := json.Marshal(data)
		if err != nil {
			r.Errors = gqlerror.List{gqlerror.Wrap(err)}
			return
		}

		if err := json.Unmarshal(dataJson, &r.Data); err != nil {
			r.Errors = gqlerror.List{gqlerror.Wrap(err)}
		}
		return
	}

	// 常规GraphQL查询处理
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

// isIntrospectionQuery 判断是否是自省查询
func isIntrospectionQuery(query string) bool {
	// 检查是否包含__schema或__type查询
	return strings.Contains(query, "__schema") || strings.Contains(query, "__type")
}
