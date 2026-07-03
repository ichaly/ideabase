// Package gql 提供了GraphQL到SQL的编译和执行功能
// 核心功能包括将GraphQL查询编译为高效SQL、执行查询并返回结果，以及支持多种SQL方言
package gql

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofiber/fiber/v3"
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
		Data   map[string]interface{} `json:"data,omitempty"`   // 成功结果数据
		Errors gqlerror.List          `json:"errors,omitempty"` // 错误信息列表
		raw    []byte                 // 直通字节：无resolver时DB返回的__root JSON原样输出
	}
)

// MarshalJSON 直通快路径：__root字节直接拼入响应，免解包重序列化
// （典型列表响应实测省~0.7ms与上万次分配，HTTP与订阅推送共用）
func (my gqlReply) MarshalJSON() ([]byte, error) {
	if my.raw == nil || len(my.Errors) > 0 {
		type alias gqlReply // 别名擦除方法集，避免递归
		return json.Marshal(alias(my))
	}
	data := my.raw
	if len(data) == 0 {
		data = []byte(`{}`)
	}
	buf := make([]byte, 0, len(data)+9)
	buf = append(buf, `{"data":`...)
	buf = append(buf, data...)
	return append(buf, '}'), nil
}

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
	actions   map[string]Action   // 操作级Action注册表：顶层字段名 -> 实现
	source    string              // 原始schema文本，RegisterAction合并SDL时重建的基底
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
		actions:   make(map[string]Action),
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

	executor.source = data
	executor.schema = s
	executor.intro = intro.New(s)

	if d != nil {
		// 全文搜索能力：配置优先，否则按驱动从注册表探测（无探测器的数据库降级ilike）
		if mode := strings.TrimSpace(m.cfg.Search.Mode); mode != "" {
			executor.metadata.SetSearchMode(mode, m.cfg.Search.Config)
		} else if detect, ok := searchDetectors[d.Name()]; ok {
			executor.metadata.SetSearchMode(detect(d))
		} else {
			executor.metadata.SetSearchMode("ilike", "")
		}
		// 订阅唤醒源：按驱动名从注册表选取CDC实现（复制连接延迟到首个订阅时建立）
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
// 目录是可选的：不存在则跳过（无持久化查询不影响服务启动）
func (my *Executor) LoadDocuments(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
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
		// 复用已解析的AST预热编译缓存（不再重复解析文档）；
		// 依赖变量内容的操作（volatile）缓存AST，执行期免解析重编译
		operation.SelectionSet = inline(operation.SelectionSet, doc.Fragments)
		if hit, _ := my.checkActions(operation.SelectionSet); hit {
			continue // Action操作无SQL计划，执行期走分发路径
		}
		_, _ = my.compile(planKey{operation: operation.Name, query: content}, operation, nil)
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
	if strings.TrimSpace(req.Query) == "" && req.OperationName != "" {
		query, ok := my.documents[req.OperationName]
		if !ok {
			return c.JSON(gqlReply{Errors: gqlerror.List{gqlerror.Errorf("未找到名为'%s'的持久化操作", req.OperationName)}})
		}
		req.Query = query
	}

	// 返回结果（无resolver路径经MarshalJSON直通输出）
	return c.JSON(my.execute(c.Context(), req.Query, req.Variables, req.OperationName))
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
	r := my.execute(ctx, query, variables, operationName)
	// 公开API契约：Data始终可编程访问（直通字节解包回map）
	if r.raw != nil {
		result := make(map[string]interface{})
		if len(r.raw) > 0 {
			if err := jsonNumeric.Unmarshal(r.raw, &result); err != nil {
				return gqlReply{Errors: gqlerror.List{gqlerror.Wrap(err)}}
			}
			normalizeNumbers(result)
		}
		r.Data, r.raw = result, nil
	}
	return r
}

// execute 执行核心：无resolver的成功结果以直通字节形态返回（raw）
func (my *Executor) execute(ctx context.Context, query string, variables map[string]interface{}, operationName string) gqlReply {
	var r gqlReply

	// 获取执行计划（缓存命中零解析）；缓存未命中时解析一次，
	// 顶层选择集含__schema/__type则路由到自省投影，否则编译为SQL
	plan, err := my.plan(query, operationName, variables)
	if err != nil {
		if introErr, ok := err.(*introQuery); ok {
			data, ierr := my.intro.Introspect(introErr.operation, variables)
			if ierr != nil {
				r.Errors = gqlerror.List{gqlerror.Wrap(ierr)}
			} else {
				r.Data = data
			}
			return r
		}
		if actionErr, ok := err.(*actionQuery); ok {
			return my.executeActions(ctx, actionErr.operation, variables)
		}
		r.Errors = gqlerror.List{gqlerror.Wrap(err)}
		return r
	}

	data, err := my.fetch(ctx, plan, variables)
	if err != nil {
		r.Errors = gqlerror.List{gqlerror.Wrap(err)}
		return r
	}

	// 无resolver时跳过解包，序列化期直通输出
	if len(plan.resolvers) == 0 {
		r.raw = data
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
func (my *Executor) fetch(ctx context.Context, plan *Plan, variables map[string]interface{}) ([]byte, error) {
	args := plan.Args(variables, scopeValues(ctx)) // 行级作用域值从请求上下文取
	var data []byte
	err := my.database.WithContext(ctx).Raw(plan.SQL, args...).Row().Scan(&data)
	return data, err
}

// unpack 解包__root JSON为data（顶层key即字段别名）并执行resolver后处理
func (my *Executor) unpack(ctx context.Context, plan *Plan, data []byte) (map[string]interface{}, error) {
	result := make(map[string]interface{})
	if len(data) > 0 {
		if err := jsonNumeric.Unmarshal(data, &result); err != nil {
			return nil, err
		}
		normalizeNumbers(result)
	}
	// 调用方已保证 resolvers 非空（无resolver走直通路径不进此函数）
	return result, my.resolve(ctx, plan.resolvers, result)
}

// normalizeNumbers 就地把json.Number收敛为int64/float64：
// 整数走int64无损(bigint主键如雪花ID>2^53经float64必丢精度)，非整数才降级float64。
func normalizeNumbers(v interface{}) interface{} {
	switch val := v.(type) {
	case stdjson.Number:
		if i, err := val.Int64(); err == nil {
			return i
		}
		if f, err := val.Float64(); err == nil {
			return f
		}
		return val.String()
	case map[string]interface{}:
		for k, item := range val {
			val[k] = normalizeNumbers(item)
		}
	case []interface{}:
		for i, item := range val {
			val[i] = normalizeNumbers(item)
		}
	}
	return v
}

// introQuery 解析后发现是自省查询：经error通道带出已解析的operation，
// 调用方（Execute）路由到自省投影，其余入口（订阅/持久化预热）按错误处理
type introQuery struct {
	operation *ast.OperationDefinition
}

func (my *introQuery) Error() string { return "自省查询不支持此入口" }

// plan 获取执行计划：命中缓存零解析；volatile命中仅重做SQL构建；
// 未命中则解析一次（fragment就地展开），自省查询经introQuery带出
func (my *Executor) plan(query, operationName string, variables map[string]interface{}) (*Plan, error) {
	key := planKey{operation: operationName, query: query}
	if entry, ok := my.cache.Get(key); ok {
		if entry.action {
			return nil, &actionQuery{operation: entry.operation}
		}
		if entry.plan != nil {
			return entry.plan, nil
		}
		// volatile：仅重做SQL构建，binding复用缓存（不依赖变量）
		plan, err := my.compiler.Compile(entry.operation, variables)
		if err != nil {
			return nil, err
		}
		plan.resolvers = entry.resolvers
		return plan, nil
	}

	operation, err := my.parse(query, operationName)
	if err != nil {
		return nil, err
	}
	if hasIntroField(operation.SelectionSet) {
		return nil, &introQuery{operation: operation}
	}
	if hit, err := my.checkActions(operation.SelectionSet); hit {
		if err != nil {
			return nil, err
		}
		my.cache.Put(key, &planEntry{operation: operation, action: true})
		return nil, &actionQuery{operation: operation}
	}
	if my.compiler == nil || my.database == nil {
		return nil, fmt.Errorf("执行器未配置数据库或编译器")
	}
	return my.compile(key, operation, variables)
}

// parse 解析校验查询并选定操作，fragment就地展开为纯字段选择集
func (my *Executor) parse(query, operationName string) (*ast.OperationDefinition, error) {
	doc, errs := gqlparser.LoadQuery(my.schema, query)
	if len(errs) > 0 {
		return nil, errs
	}
	operation := doc.Operations.ForName(operationName)
	if operation == nil {
		if operationName != "" || len(doc.Operations) != 1 {
			return nil, fmt.Errorf("未找到名为'%s'的操作（多操作查询必须提供operationName）", operationName)
		}
		operation = doc.Operations[0]
	}
	operation.SelectionSet = inline(operation.SelectionSet, doc.Fragments)
	return operation, nil
}

// compile 编译并缓存：resolver绑定在此一次性收集（不依赖变量内容）；
// volatile计划SQL不可复用但AST与绑定可以——缓存供后续请求免解析重编译
func (my *Executor) compile(key planKey, operation *ast.OperationDefinition, variables map[string]interface{}) (*Plan, error) {
	plan, err := my.compiler.Compile(operation, variables)
	if err != nil {
		return nil, err
	}
	plan.resolvers = collectBindings(my.metadata, operation)
	if plan.Volatile() {
		my.cache.Put(key, &planEntry{operation: operation, resolvers: plan.resolvers})
	} else {
		my.cache.Put(key, &planEntry{plan: plan})
	}
	return plan, nil
}

// hasIntroField 顶层选择集是否含自省字段（fragment已展开）
func hasIntroField(set ast.SelectionSet) bool {
	for _, s := range set {
		if f, ok := s.(*ast.Field); ok && (f.Name == "__schema" || f.Name == "__type") {
			return true
		}
	}
	return false
}
