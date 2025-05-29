// Package gql 提供了GraphQL到SQL的编译和执行功能
// 核心功能包括将GraphQL查询编译为高效SQL、执行查询并返回结果，以及支持多种SQL方言
package gql

import (
	"context"
	"fmt"

	"github.com/duke-git/lancet/v2/strutil"
	"github.com/gofiber/fiber/v2"
	"github.com/ichaly/ideabase/gql/internal/intro"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"gorm.io/gorm"
)

// 请求和结果类型定义
type (
	// gqlQuery 表示来自客户端的GraphQL请求
	// 包含查询文本、操作名称和变量
	gqlQuery struct {
		Query         string                 `json:"query"`         // GraphQL查询文本
		OperationName string                 `json:"operationName"` // 要执行的操作名称，多操作查询时必须
		Variables     map[string]interface{} `json:"variables"`     // 查询变量
	}

	// gqlReply 表示GraphQL响应
	// 包含执行结果数据或错误信息
	gqlReply struct {
		sql    string                 // 生成的SQL语句，仅内部使用
		args   []any                  // SQL参数，仅内部使用
		Data   map[string]interface{} `json:"data,omitempty"`   // 成功结果数据
		Errors gqlerror.List          `json:"errors,omitempty"` // 错误信息列表
	}
)

// Executor GraphQL执行器
// 负责解析GraphQL查询、编译为SQL并执行查询，支持多种数据库方言
// 可作为Fiber插件集成到Web服务中，提供标准的GraphQL API
type Executor struct {
	intro    *intro.Handler // 自省处理器，处理__schema和__type查询
	schema   *ast.Schema    // GraphQL模式定义
	database *gorm.DB       // 数据库连接，用于执行生成的SQL
	metadata *Metadata      // 元数据信息，包含表结构、关系等
	compiler *Compiler      // 编译器，将GraphQL查询编译为SQL
}

// 构造函数和初始化方法

// NewExecutor 创建一个新的GraphQL执行器实例
// 参数:
//   - d: 数据库连接(gorm.DB)
//   - r: GraphQL模式渲染器
//   - m: 数据库元数据
//
// 返回:
//   - 执行器实例和可能的错误
//
// 使用示例:
//
//	renderer := gql.NewRenderer(metadata)
//	executor, err := gql.NewExecutor(db, renderer, metadata)
//	if err != nil {
//	    log.Fatal(err)
//	}
func NewExecutor(d *gorm.DB, r *Renderer, m *Metadata, c *Compiler) (*Executor, error) {
	executor := &Executor{
		database: d,
		metadata: m,
		compiler: c,
	}

	// 生成并加载GraphQL模式
	data, err := r.Generate()
	if err != nil {
		return nil, err
	}
	s, err := gqlparser.LoadSchema(&ast.Source{
		Name:  "schema.graphql",
		Input: data,
	})
	if err != nil {
		return nil, err
	}

	executor.schema = s
	executor.intro = intro.New(s)
	return executor, nil
}

// 接口实现方法

// Path 实现Plugin接口的Path方法，返回插件的基础路径
// 返回: GraphQL API的基础路径 ("/graphql")
// 此方法使Executor能够作为Fiber的插件集成
func (my *Executor) Path() string {
	return "/graphql"
}

// 绑定插件路由
// Bind 实现Plugin接口的Bind方法，注册GraphQL HTTP处理路由
// 参数:
//   - r: Fiber路由器，用于注册路由
func (my *Executor) Bind(r fiber.Router) {
	// 注册GraphQL请求处理路由
	r.Post("/", my.Handler)
}

// Handler 处理GraphQL HTTP请求
// 作为Fiber中间件函数，解析请求体中的GraphQL查询并执行
// 参数:
//   - c: Fiber上下文，包含HTTP请求和响应信息
//
// 返回:
//   - 可能的错误信息
//
// 使用示例:
//
//	app.Post("/graphql", executor.Handler)
func (my *Executor) Handler(c *fiber.Ctx) error {
	// 解析请求
	var req gqlQuery
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"errors": []gqlerror.Error{*gqlerror.Wrap(err)},
		})
	}

	// 直接使用map类型的变量
	result := my.Execute(c.Context(), req.Query, req.Variables, req.OperationName)

	// 返回结果
	return c.JSON(result)
}

// 主要公开方法

// Execute 执行GraphQL查询并返回结果
// 支持标准GraphQL查询、变量和操作名，自动处理自省查询
// 参数:
//   - ctx: 上下文对象，可用于取消操作或传递请求信息
//   - query: GraphQL查询文本
//   - variables: 查询变量(可选)
//   - operationName: 要执行的操作名称(多操作查询时必须)
//
// 返回:
//   - 包含查询结果或错误信息的GraphQL响应
//
// 使用示例:
//
//	result := executor.Execute(context.Background(),
//	    "query { user(id: 1) { name email } }",
//	    nil, "")
func (my *Executor) Execute(ctx context.Context, query string, variables map[string]interface{}, operationName string) gqlReply {
	var r gqlReply

	// 处理自省查询
	if strutil.ContainsAny(query, []string{"__schema", "__type"}) {
		data, err := my.intro.Introspect(ctx, query, variables)
		if err != nil {
			r.Errors = gqlerror.List{gqlerror.Wrap(err)}
			return r
		}

		r.Data = data
		return r
	}

	// 解析查询
	doc, err := gqlparser.LoadQuery(my.schema, query)
	if err != nil {
		r.Errors = gqlerror.List{gqlerror.Wrap(err)}
		return r
	}

	// 按照GraphQL规范处理操作
	operation, opErr := getOperation(doc.Operations, operationName)
	if opErr != nil {
		r.Errors = gqlerror.List{gqlerror.Wrap(opErr)}
		return r
	}

	// 执行选定的操作
	r = my.runOperation(operation, variables)
	return r
}

// 获取操作
// getOperation 根据GraphQL标准从操作列表中选择要执行的操作
// 根据GraphQL规范:
// 1. 如果只有一个操作，直接返回该操作
// 2. 如果有多个操作，必须通过operationName指定要执行哪个
// 3. 如果指定的operationName未找到，返回错误
//
// 参数:
//   - operations: GraphQL操作列表
//   - operationName: 要执行的操作名称(多操作时必须)
//
// 返回:
//   - 选定的操作定义和可能的错误
func getOperation(operations ast.OperationList, operationName string) (*ast.OperationDefinition, error) {
	// 单操作直接返回，多操作需要操作名
	if len(operations) == 1 {
		return operations[0], nil
	} else if operationName == "" {
		return nil, fmt.Errorf("必须提供operationName，因为该查询包含多个操作")
	}

	// 查找指定操作
	for _, op := range operations {
		if op.Name == operationName {
			return op, nil
		}
	}
	return nil, fmt.Errorf("未找到名为'%s'的操作", operationName)
}

// runOperation 执行单个GraphQL操作
// 将操作编译为SQL并执行，然后处理结果
// 参数:
//   - op: 要执行的GraphQL操作定义
//   - variables: 操作变量
//
// 返回:
//   - 包含执行结果或错误的GraphQL响应
func (my *Executor) runOperation(operation *ast.OperationDefinition, variables map[string]interface{}) gqlReply {
	var r gqlReply

	// 编译并执行SQL查询
	var err error
	if r.sql, r.args, err = my.compiler.Build(operation, variables); err != nil {
		r.Errors = append(r.Errors, gqlerror.Wrap(err))
		return r
	}

	result := make(map[string]interface{})
	if err = my.database.Raw(r.sql, r.args...).Scan(&result).Error; err != nil {
		r.Errors = append(r.Errors, gqlerror.Wrap(err))
		return r
	}

	// 按GraphQL规范组织结果
	if operation.Name != "" {
		r.Data = map[string]interface{}{operation.Name: result}
	} else {
		r.Data = result
	}

	return r
}
