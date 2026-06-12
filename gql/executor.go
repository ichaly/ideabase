// Package gql 提供了GraphQL到SQL的编译和执行功能
// 核心功能包括将GraphQL查询编译为高效SQL、执行查询并返回结果，以及支持多种SQL方言
package gql

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/ichaly/ideabase/gql/internal/intro"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"gorm.io/gorm"
)

// introPattern 自省查询特征：__schema字段 或 __type(调用；不会误伤__typename
var introPattern = regexp.MustCompile(`__schema\b|__type\s*\(`)

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
	intro     *intro.Handler      // 自省处理器，处理__schema和__type查询
	schema    *ast.Schema         // GraphQL模式定义
	database  *gorm.DB            // 数据库连接，用于执行生成的SQL
	metadata  *Metadata           // 元数据信息，包含表结构、关系等
	compiler  *Compiler           // 编译器，将GraphQL查询编译为SQL
	cache     *planCache          // 执行计划缓存，命中路径零解析零编译
	resolvers map[string]Resolver // 自定义字段解析器注册表
	documents map[string]string   // 持久化查询文档：操作名 -> 查询文本
	cdc       notifier            // CDC唤醒源（按数据库驱动从注册表选取）
}

// Register 注册自定义字段解析器，与元数据中 Field.Resolver 按名绑定
func (my *Executor) Register(resolvers ...Resolver) {
	for _, r := range resolvers {
		my.resolvers[r.Name()] = r
	}
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
		database:  d,
		metadata:  m,
		compiler:  c,
		cache:     newPlanCache(512),
		resolvers: make(map[string]Resolver),
		documents: make(map[string]string),
	}

	// 加载GraphQL模式：配置了schema.file优先从文件加载（生产推荐），否则由renderer生成
	data, err := loadSchema(m, r)
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

	// 订阅唤醒源：按驱动名从注册表选取CDC实现（复制连接延迟到首个订阅时建立）
	if d != nil {
		if factory, ok := notifiers[d.Name()]; ok {
			if source, err := factory(d, m.cfg.Subscription); err == nil {
				executor.cdc = source
			}
		}
	}
	return executor, nil
}

// loadSchema 解析schema来源：schema.file配置 > renderer现场生成
func loadSchema(m *Metadata, r *Renderer) (string, error) {
	if file := strings.TrimSpace(m.cfg.Schema.File); file != "" {
		if !filepath.IsAbs(file) {
			file = filepath.Join(m.cfg.Root, file)
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("读取schema文件失败: %w", err)
		}
		return string(data), nil
	}
	return r.Generate()
}

// LoadDocuments 从目录加载.graphql操作文档（持久化查询）
// 操作按名注册，可通过ExecuteOperation按名执行；编译缓存尽力预热
func (my *Executor) LoadDocuments(dir string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".graphql") {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return my.loadDocument(string(content))
	})
}

// loadDocument 解析并注册文档中的命名操作
func (my *Executor) loadDocument(content string) error {
	doc, errs := gqlparser.LoadQuery(my.schema, content)
	if len(errs) > 0 {
		return errs
	}
	for _, operation := range doc.Operations {
		if operation.Name == "" {
			return fmt.Errorf("持久化文档中的操作必须命名")
		}
		my.documents[operation.Name] = content
		// 尽力预热编译缓存；依赖变量内容的操作（volatile）留到执行期编译
		_, _ = my.plan(content, operation.Name, nil)
	}
	return nil
}

// ExecuteOperation 按操作名执行已加载文档中的持久化查询
func (my *Executor) ExecuteOperation(ctx context.Context, operationName string, variables map[string]interface{}) gqlReply {
	query, ok := my.documents[operationName]
	if !ok {
		return gqlReply{Errors: gqlerror.List{gqlerror.Errorf("未找到名为'%s'的持久化操作", operationName)}}
	}
	return my.Execute(ctx, query, variables, operationName)
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
	// 注册GraphQL请求处理路由；GET用于订阅WebSocket升级
	r.Post("/", my.Handler)
	r.Get("/", my.SubscribeHandler)
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
func (my *Executor) Handler(c fiber.Ctx) error {
	// 解析请求
	var req gqlQuery
	if err := c.Bind().Body(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"errors": []gqlerror.Error{*gqlerror.Wrap(err)},
		})
	}

	// 空查询且携带操作名时按持久化查询执行
	var result gqlReply
	if strings.TrimSpace(req.Query) == "" && req.OperationName != "" {
		result = my.ExecuteOperation(c.Context(), req.OperationName, req.Variables)
	} else {
		result = my.Execute(c.Context(), req.Query, req.Variables, req.OperationName)
	}

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

	// 处理自省查询（精确匹配__schema/__type字段，__typename走正常编译路径）
	if introPattern.MatchString(query) {
		data, err := my.intro.Introspect(ctx, query, variables, operationName)
		if err != nil {
			r.Errors = gqlerror.List{gqlerror.Wrap(err)}
			return r
		}

		r.Data = data
		return r
	}

	// 获取执行计划（优先命中缓存）并执行
	plan, err := my.plan(query, operationName, variables)
	if err != nil {
		r.Errors = gqlerror.List{gqlerror.Wrap(err)}
		return r
	}

	data, args, err := my.fetch(ctx, plan, variables)
	r.sql, r.args = plan.SQL, args
	if err != nil {
		r.Errors = gqlerror.List{gqlerror.Wrap(err)}
		return r
	}

	result, err := my.unpack(ctx, plan, data)
	if err != nil {
		r.Errors = gqlerror.List{gqlerror.Wrap(err)}
		return r
	}
	r.Data = result
	return r
}

// fetch 执行计划：单条SQL返回单行单列的__root JSON原始字节
func (my *Executor) fetch(ctx context.Context, plan *Plan, variables map[string]interface{}) ([]byte, []any, error) {
	args := plan.Args(variables)
	var data []byte
	err := my.database.WithContext(ctx).Raw(plan.SQL, args...).Row().Scan(&data)
	return data, args, err
}

// unpack 解包__root JSON为data（顶层key即字段别名）并执行resolver后处理
func (my *Executor) unpack(ctx context.Context, plan *Plan, data []byte) (map[string]interface{}, error) {
	result := make(map[string]interface{})
	if len(data) > 0 {
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, err
		}
	}
	if len(plan.resolvers) > 0 {
		if err := my.resolve(ctx, plan.resolvers, result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// plan 获取执行计划：命中缓存直接返回，未命中则解析编译并缓存
func (my *Executor) plan(query, operationName string, variables map[string]interface{}) (*Plan, error) {
	if my.compiler == nil || my.database == nil {
		return nil, fmt.Errorf("执行器未配置数据库或编译器")
	}

	key := operationName + "\x00" + query
	if plan, ok := my.cache.Get(key); ok {
		return plan, nil
	}

	doc, errs := gqlparser.LoadQuery(my.schema, query)
	if len(errs) > 0 {
		return nil, errs
	}
	operation, err := getOperation(doc.Operations, operationName)
	if err != nil {
		return nil, err
	}
	// fragment展开后编译器只需处理纯字段选择集
	operation.SelectionSet = inline(operation.SelectionSet, doc.Fragments)

	plan, err := my.compiler.Compile(operation, variables)
	if err != nil {
		return nil, err
	}
	// volatile计划依赖变量内容（如整体input变量），不可复用
	if !plan.Volatile() {
		my.cache.Put(key, plan)
	}
	return plan, nil
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
