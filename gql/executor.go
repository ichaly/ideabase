package gql

import (
	"context"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/ichaly/ideabase/gql/internal/intro"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"gorm.io/gorm"
)

// 请求和结果类型定义
type (
	gqlRequest struct {
		Query         string                 `json:"query"`
		OperationName string                 `json:"operationName"`
		Variables     map[string]interface{} `json:"variables"`
	}
	gqlResult struct {
		sql    string
		args   []any
		Data   map[string]interface{} `json:"data,omitempty"`
		Errors gqlerror.List          `json:"errors,omitempty"`
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
func NewExecutor(d *gorm.DB, r *Renderer, c *Compiler) (*Executor, error) {
	data, err := r.Generate()
	if err != nil {
		return nil, err
	}
	s, err := gqlparser.LoadSchema(&ast.Source{
		Name:  "schema.graphql",
		Input: string(data),
	})
	if err != nil {
		return nil, err
	}
	i := intro.New(s)
	return &Executor{
		db:       d,
		intro:    i,
		schema:   s,
		compiler: c,
	}, nil
}

// Base 实现Plugin接口的Base方法，返回插件的基础路径
func (my *Executor) Base() string {
	return "/graphql"
}

// Init 实现Plugin接口的Init方法，初始化插件
func (my *Executor) Init(r fiber.Router) {
	// 注册GraphQL请求处理路由
	r.Post("/", my.Handler)
}

// Handler 处理GraphQL请求
func (my *Executor) Handler(c *fiber.Ctx) error {
	// 解析请求
	var req gqlRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"errors": []gqlerror.Error{*gqlerror.Wrap(err)},
		})
	}

	// 将变量转换为RawMessage
	var variables RawMessage
	if len(req.Variables) > 0 {
		var err error
		variables, err = json.Marshal(req.Variables)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"errors": []gqlerror.Error{*gqlerror.Wrap(err)},
			})
		}
	}

	// 执行GraphQL查询
	result := my.Execute(c.Context(), req.Query, variables)

	// 返回结果
	return c.JSON(result)
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

		r.Data = data
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
