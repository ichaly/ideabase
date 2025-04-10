// Package gql 提供了GraphQL到SQL的编译和执行功能
// 核心功能包括将GraphQL查询编译为高效SQL、执行查询并返回结果，以及支持多种SQL方言
package gql

import (
	"context"
	"fmt"
	"strings"

	"github.com/duke-git/lancet/v2/strutil"
	"github.com/gofiber/fiber/v2"
	"github.com/ichaly/ideabase/gql/internal/intro"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"gorm.io/gorm"
)

// 方言注册表，存储所有注册的SQL方言实现
// 不同的数据库(如PostgreSQL、MySQL等)需要不同的SQL生成规则，通过方言机制实现
var dialects = make(map[string]Dialect)

// RegisterDialect 全局注册方法，用于各个方言包在init函数中调用
// 方言包应在其init函数中调用此方法注册自己，例如:
//
//	func init() {
//	    gql.RegisterDialect("postgresql", &PostgreSQLDialect{})
//	}
//
// name: 方言名称，如"postgresql"、"mysql"
// dialect: 方言实现，需实现Dialect接口
func RegisterDialect(name string, dialect Dialect) {
	dialects[name] = dialect
}

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
	db      *gorm.DB       // 数据库连接，用于执行生成的SQL
	meta    *Metadata      // 元数据信息，包含表结构、关系等
	intro   *intro.Handler // 自省处理器，处理__schema和__type查询
	schema  *ast.Schema    // GraphQL模式定义
	dialect Dialect        // SQL方言实现
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
func NewExecutor(d *gorm.DB, r *Renderer, m *Metadata) (*Executor, error) {
	executor := &Executor{
		db:   d,
		meta: m,
	}

	// 选择SQL方言
	if err := executor.selectDialect(); err != nil {
		return nil, err
	}

	// 生成并加载GraphQL模式
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

	executor.schema = s
	executor.intro = intro.New(s)
	return executor, nil
}

// selectDialect 选择适合当前数据库的SQL方言
// 方言选择逻辑:
// 1. 优先根据数据库驱动类型选择对应方言
// 2. 如未找到匹配，尝试使用PostgreSQL方言(推荐方言)
// 3. 如仍未找到，使用首个可用方言
// 4. 如无可用方言，返回错误
// 返回: 如无可用方言则返回错误
func (my *Executor) selectDialect() error {
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
		return fmt.Errorf("没有可用的SQL方言实现，请确保导入了相应的dialect包")
	}

	return nil
}

// 接口实现方法

// Base 实现Plugin接口的Base方法，返回插件的基础路径
// 返回: GraphQL API的基础路径 ("/graphql")
// 此方法使Executor能够作为Fiber的插件集成
func (my *Executor) Base() string {
	return "/graphql"
}

// 初始化插件
// Init 实现Plugin接口的Init方法，注册GraphQL HTTP处理路由
// 参数:
//   - r: Fiber路由器，用于注册路由
func (my *Executor) Init(r fiber.Router) {
	// 注册GraphQL请求处理路由
	r.Post("/", my.Handler)
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

// compile 将GraphQL操作编译为SQL语句和参数
// 此方法是一个内部辅助方法，不直接暴露给外部使用
// 参数:
//   - operation: 要编译的GraphQL操作定义
//   - variables: 操作变量
//
// 返回:
//   - 编译后的SQL语句和参数列表
func (my *Executor) compile(operation *ast.OperationDefinition, variables map[string]interface{}) (string, []interface{}) {
	cpl := NewCompiler(my.meta)
	defer cpl.Release()      // 使用完毕后释放回对象池
	cpl.dialect = my.dialect // 设置共享的方言实现
	cpl.Build(operation, variables)
	return cpl.String(), cpl.Args()
}

// runOperation 执行单个GraphQL操作
// 将操作编译为SQL并执行，然后处理结果
// 参数:
//   - op: 要执行的GraphQL操作定义
//   - variables: 操作变量
//
// 返回:
//   - 包含执行结果或错误的GraphQL响应
func (my *Executor) runOperation(op *ast.OperationDefinition, variables map[string]interface{}) gqlReply {
	var r gqlReply

	// 编译并执行SQL查询
	r.sql, r.args = my.compile(op, variables)

	result := make(map[string]interface{})
	if err := my.db.Raw(r.sql, r.args...).Scan(&result).Error; err != nil {
		r.Errors = append(r.Errors, gqlerror.Wrap(err))
		return r
	}

	// 按GraphQL规范组织结果
	if op.Name != "" {
		r.Data = map[string]interface{}{op.Name: result}
	} else {
		r.Data = result
	}

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
