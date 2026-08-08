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
	"sync"
	"sync/atomic"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/internal/intro"
	"github.com/ichaly/ideabase/log"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

// planCacheSize 执行计划LRU缓存容量（NewExecutor与Register重建共用）
const planCacheSize = 512

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
		return jsonReply.Marshal(alias(my))
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
	intro        *intro.Handler      // 自省处理器，处理__schema和__type查询
	schema       *ast.Schema         // GraphQL模式定义
	database     *gorm.DB            // 数据库连接，用于执行生成的SQL
	metadata     *Metadata           // 元数据信息，包含表结构、关系等
	compiler     *Compiler           // 编译器，将GraphQL查询编译为SQL
	cache        *planCache          // 执行计划缓存，命中路径零解析零编译
	flight       singleflight.Group  // 未命中收敛：并发的同一冷查询只解析编译一次
	feedMu       sync.Mutex          // 保护feeds及各feed的订阅者集合
	feeds        map[string]*feed    // 共享订阅流：同构订阅（查询+变量+作用域）共用一次重查
	resolvers    map[string]Resolver // 自定义字段解析器注册表
	source       string              // 原始schema文本，Register合并注册声明时重建的基底
	documents    map[string]string   // 持久化查询文档：操作名 -> 查询文本
	remotes      map[string]Remote   // 远程数据源注册表：数据源名 -> 实现
	cdc          notifier            // CDC唤醒源（按数据库驱动从注册表选取）
	frozen       atomic.Bool         // 首次执行后冻结注册表（rebuild与serving不互斥，运行期注册即数据竞争）
	finalizeOnce sync.Once
	snapshot     *Snapshot
	snapshotErr  error
}

// options 读取schema级选项（元数据未配置时为零值安全默认）
func (my *Executor) options() internal.SchemaConfig {
	if my.metadata == nil || my.metadata.cfg == nil {
		return internal.SchemaConfig{Introspection: true}
	}
	return my.metadata.cfg.Schema
}

// Close 释放后台资源：CDC复制连接与重连循环退出；
// 存续的共享订阅流不再被唤醒，随各订阅者ctx结束
func (my *Executor) Close() {
	if my.cdc != nil {
		my.cdc.close()
	}
}

// Register 统一注册入口：Resolver/Remote 按实现的接口路由到对应注册表，
// 字段级声明挂载进宿主实体并重建schema。仅限启动期调用：
// 重建schema/自省/计划缓存的过程不与并发请求互斥，首次执行后冻结报错
func (my *Executor) Register(items ...any) error {
	if err := my.mutable("Register"); err != nil {
		return err
	}
	for _, item := range items {
		switch v := item.(type) {
		case Resolver:
			if v.Define().Existing {
				return fmt.Errorf("resolver %s绑定已有字段，请使用Replace显式覆盖", v.Name())
			}
			if _, exists := my.resolvers[v.Name()]; exists {
				return fmt.Errorf("resolver已注册: %s", v.Name())
			}
			my.resolvers[v.Name()] = v
		case Remote:
			my.remotes[v.Name()] = v
		default:
			return fmt.Errorf("不支持的注册类型: %T", item)
		}
		if m, ok := item.(mounted); ok {
			if err := my.mount(m); err != nil {
				return err
			}
		}
	}
	return my.rebuild()
}

// Replace 显式替换schema已有字段的默认实现。默认数据库CRUD仍保持零代码快路径，
// 只有被替换的coordinate才进入Resolver分发。
func (my *Executor) Replace(resolver Resolver) error {
	if err := my.mutable("Replace"); err != nil {
		return err
	}
	d := resolver.Define()
	if !d.Existing {
		return fmt.Errorf("resolver %s必须使用Existing选项声明覆盖已有字段", resolver.Name())
	}
	if d.Class == "Query" || d.Class == "Mutation" {
		kind := my.schema.Types[d.Class]
		if kind == nil || kind.Fields.ForName(d.Name) == nil {
			return fmt.Errorf("schema字段不存在: %s.%s", d.Class, d.Name)
		}
	} else {
		class, ok := my.metadata.GetNode(d.Class)
		if !ok || class.Fields[d.Name] == nil {
			return fmt.Errorf("实体字段不存在: %s.%s", d.Class, d.Name)
		}
		class.Fields[d.Name].Resolver = resolver.Name()
	}
	my.resolvers[resolver.Name()] = resolver
	return my.rebuild()
}

// Wrap 用强类型中间件增强已注册Resolver，先注册的包装位于内层；仅限启动期。
func (my *Executor) Wrap(coordinate string, middleware ResolverMiddleware) error {
	if err := my.mutable("Wrap"); err != nil {
		return err
	}
	resolver, ok := my.resolvers[coordinate]
	if !ok {
		return fmt.Errorf("resolver未注册: %s", coordinate)
	}
	my.resolvers[coordinate] = middleware(resolver)
	my.cache = newPlanCache(planCacheSize)
	return nil
}

func (my *Executor) mutable(operation string) error {
	if my.frozen.Load() {
		return fmt.Errorf("注册表已冻结：%s仅限启动期调用", operation)
	}
	return nil
}

// Snapshot 返回执行器实际使用的不可变Schema/Handler契约，并冻结启动期注册表。
func (my *Executor) Snapshot() (*Snapshot, error) {
	my.finalizeOnce.Do(func() {
		my.snapshot, my.snapshotErr = buildSnapshot(my)
		my.frozen.Store(true)
	})
	return my.snapshot, my.snapshotErr
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
	if m == nil || r == nil {
		return nil, fmt.Errorf("缺少Metadata或Renderer")
	}
	trusted := m.legacyTrusted
	if m.cfg == nil || !m.cfg.IsDebug() {
		if err := requirePolicies(m, trusted, "LegacyTrustedBoundaryPolicy"); err != nil {
			return nil, err
		}
	}
	if m != nil && m.legacyTrusted {
		log.Warn().Msg("已启用LegacyTrustedBoundaryPolicy；该兼容边界不适用于多租户生产服务")
	}
	data, err := loadSchema(m, r)
	if err != nil {
		return nil, err
	}
	return newExecutor(d, m, c, data)
}

func newExecutor(d *gorm.DB, m *Metadata, c *Compiler, data string) (*Executor, error) {
	executor := &Executor{
		database:  d,
		metadata:  m,
		compiler:  c,
		cache:     newPlanCache(planCacheSize),
		feeds:     make(map[string]*feed),
		resolvers: make(map[string]Resolver),
		documents: make(map[string]string),
		remotes:   make(map[string]Remote),
	}

	s, err := gqlparser.LoadSchema(&ast.Source{
		Name:  "schema.graphql",
		Input: data,
	})
	if err != nil {
		return nil, err
	}

	// gqlparser exposes @defer as a built-in directive, but this engine does not
	// implement incremental delivery. Removing it keeps validation and
	// introspection honest without changing gqlparser's global rule set.
	disableUnsupportedDirectives(s)
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
			} else {
				log.Warn().Err(err).Str("driver", d.Name()).Msg("CDC唤醒源初始化失败，订阅将不可用")
			}
		}
	}
	return executor, nil
}

func disableUnsupportedDirectives(schema *ast.Schema) {
	delete(schema.Directives, "defer")
}

// loadSchema 解析schema来源：schema.file配置 > renderer现场生成
func loadSchema(m *Metadata, r *Renderer) (string, error) {
	if data, ok, err := schemaFile(m); ok || err != nil {
		if err != nil {
			return "", err
		}
		generated, err := r.Render()
		if err != nil {
			return "", err
		}
		return data, validateSchemaArtifact(generated, data)
	}
	return r.Generate()
}

func renderSchema(m *Metadata, r *Renderer) (string, error) {
	if data, ok, err := schemaFile(m); ok || err != nil {
		if err != nil {
			return "", err
		}
		generated, err := r.Render()
		if err != nil {
			return "", err
		}
		return data, validateSchemaArtifact(generated, data)
	}
	return r.Render()
}

func schemaFile(m *Metadata) (string, bool, error) {
	if file := strings.TrimSpace(m.cfg.Schema.File); file != "" {
		if !filepath.IsAbs(file) {
			file = filepath.Join(m.cfg.Root, file)
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return "", true, fmt.Errorf("读取schema文件失败: %w", err)
		}
		return string(data), true, nil
	}
	return "", false, nil
}

// LoadDocuments 从目录加载.graphql操作文档（持久化查询）
// 操作按名注册，可通过 runOperation 按名执行；编译缓存尽力预热
// 目录是可选的：不存在则跳过（无持久化查询不影响服务启动）
func (my *Executor) LoadDocuments(dir string) error {
	if my.frozen.Load() {
		return fmt.Errorf("注册表已冻结：LoadDocuments仅限启动期调用")
	}
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
	doc, errs := gqlparser.LoadQueryWithRules(my.schema, content, nil)
	if len(errs) > 0 {
		return errs
	}
	conditional := hasConditionalDirective(content)
	for _, operation := range doc.Operations {
		if operation.Name == "" {
			return fmt.Errorf("持久化文档中的操作必须命名")
		}
		if !conditional {
			// Static documents can be guarded before prewarm. Conditional documents
			// are normalized and checked per request after skipped fields disappear.
			operation.SelectionSet = inline(operation.SelectionSet, doc.Fragments)
			if err := my.validateMaxDepth(operation); err != nil {
				return err
			}
		}
		my.documents[operation.Name] = content
		// Conditional directives depend on request variables; never prewarm a
		// directive-free AST into the shared cache for these documents.
		if conditional {
			continue
		}
		// 复用已解析的AST预热编译缓存（不再重复解析文档）；
		// 依赖变量内容的操作（volatile）缓存AST，执行期免解析重编译
		operation, introspection := splitIntrospection(operation)
		if introspection != nil && !my.options().Introspection {
			return fmt.Errorf("自省查询已关闭（schema.introspection=false）")
		}
		if operation == nil { // 自省无需SQL预热，执行期直接投影当前schema
			continue
		}
		key := planKey{operation: operation.Name, query: content}
		if my.needsRootExecution(operation) {
			roleNeeded, policyNeeded := operationGovernanceNeeds(my.metadata, operation)
			my.cache.Put(key, &planEntry{
				operation:    operation,
				intro:        introspection,
				rootResolver: true,
				roleNeeded:   roleNeeded,
				policyNeeded: policyNeeded,
			})
			continue
		}
		if _, _, err := my.compile(key, operation, introspection, nil); err != nil {
			log.Warn().Err(err).Str("operation", operation.Name).Msg("持久化文档预热编译失败，执行期将重试编译")
		}
	}
	return nil
}

// runOperation 按操作名执行已加载文档中的持久化查询
func (my *Executor) runOperation(ctx context.Context, operationName string, variables map[string]interface{}) gqlReply {
	query, ok := my.documents[operationName]
	if !ok {
		return gqlReply{Errors: gqlerror.List{gqlerror.Errorf("未找到名为'%s'的持久化操作", operationName)}}
	}
	return my.queryData(ctx, query, variables, operationName)
}

// 接口实现方法

// Path 实现Plugin接口的Path方法，返回插件的基础路径
// 返回: GraphQL API的基础路径 ("/graphql")
// 此方法使Executor能够作为Fiber的插件集成
func (my *Executor) Path() string {
	return "/graphql"
}

// 主要公开方法

// Execute 执行单个GraphQL操作并返回可直接写入HTTP响应的标准GraphQL JSON。
// 数据库生成的JSON在无Resolver时原样直通，不构造map/slice对象树；GraphQL字段错误
// 编码在响应体的errors中，返回的error仅表示响应序列化失败。
func (my *Executor) Execute(ctx context.Context, query string, variables map[string]interface{}) ([]byte, error) {
	return my.executeBytes(ctx, query, variables, "")
}

func (my *Executor) executeBytes(ctx context.Context, query string, variables map[string]interface{}, operationName string) ([]byte, error) {
	return my.execute(ctx, query, variables, operationName).MarshalJSON()
}

// queryData 仅供根Resolver回查和持久化操作内部消费；普通调用统一走Execute字节响应。
func (my *Executor) queryData(ctx context.Context, query string, variables map[string]interface{}, operationName string) gqlReply {
	r := my.execute(ctx, query, variables, operationName)
	if raw := r.raw; raw != nil {
		r.Data, r.raw = map[string]interface{}{}, nil
		if len(raw) > 0 {
			v, err := decodeValue(raw)
			if err != nil {
				return gqlReply{Errors: gqlErrors(err)}
			}
			r.Data, _ = v.(map[string]interface{})
		}
	} else if r.Data != nil {
		materialize(r.Data) // 懒解码残留的原始字节段收敛为map/切片（Data可编程访问契约）
	}
	return r
}

// decodeValue 全量解码JSON字节为通用形态并收敛数字
func decodeValue(raw []byte) (interface{}, error) {
	var v interface{}
	if err := jsonNumeric.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return normalizeNumbers(v), nil
}

// materialize 就地把懒解码保留的RawMessage段解码为通用形态；解码失败原样保留
func materialize(v interface{}) interface{} {
	switch val := v.(type) {
	case stdjson.RawMessage:
		if out, err := decodeValue(val); err == nil {
			return out
		}
		return val
	case map[string]interface{}:
		for k, e := range val {
			val[k] = materialize(e)
		}
	case []interface{}:
		for i, e := range val {
			val[i] = materialize(e)
		}
	}
	return v
}

// execute 执行核心：无resolver的成功结果以直通字节形态返回（raw）
func (my *Executor) execute(ctx context.Context, query string, variables map[string]interface{}, operationName string) gqlReply {
	var r gqlReply
	if variables == nil && strings.IndexByte(query, '$') >= 0 {
		variables = make(map[string]interface{})
	}
	if _, err := my.Snapshot(); err != nil {
		r.Errors = gqlErrors(err)
		return r
	}

	// 获取执行计划（缓存命中零解析）；缓存未命中时解析一次，
	// 顶层选择集含__schema/__type则路由到自省投影，否则编译为SQL
	plan, err := my.plan(query, operationName, variables)
	if err != nil {
		if introErr, ok := err.(*introQuery); ok {
			data, ierr := my.intro.Introspect(introErr.operation, variables)
			if ierr != nil {
				r.Errors = gqlErrors(ierr)
			} else {
				r.Data = data
			}
			return r
		}
		if rootErr, ok := err.(*rootResolverQuery); ok {
			if rootErr.entry.policyNeeded {
				if authErr := my.authorize(ctx, rootErr.entry.operation, variables); authErr != nil {
					r.Errors = gqlErrors(authErr)
					return r
				}
			}
			r = my.executeRootResolvers(ctx, rootErr.entry, variables)
			if rootErr.entry.intro != nil && r.Data != nil {
				if ierr := my.mergeIntrospection(r.Data, rootErr.entry.intro, variables); ierr != nil {
					r.Errors = append(r.Errors, gqlErrors(ierr)...)
				}
			}
			return r
		}
		r.Errors = gqlErrors(err)
		return r
	}
	if plan.policyNeeded {
		if authErr := my.authorize(ctx, plan.operation, variables); authErr != nil {
			r.Errors = gqlErrors(authErr)
			return r
		}
	}

	data, err := my.fetch(ctx, plan, variables)
	if err != nil {
		r.Errors = gqlErrors(err)
		return r
	}

	// 无resolver且无混合自省时跳过解包，序列化期直通输出
	if len(plan.resolvers) == 0 && plan.intro == nil {
		r.raw = data
		return r
	}
	var result map[string]interface{}
	var warnings gqlerror.List
	if len(plan.resolvers) == 0 {
		value, decodeErr := decodeValue(data)
		err = decodeErr
		result, _ = value.(map[string]interface{})
		if result == nil {
			result = make(map[string]interface{})
		}
	} else {
		result, warnings, err = my.unpack(ctx, plan, data, variables)
	}
	if err != nil {
		r.Errors = gqlErrors(err)
		return r
	}
	if plan.intro != nil {
		if err = my.mergeIntrospection(result, plan.intro, variables); err != nil {
			r.Errors = gqlErrors(err)
			return r
		}
	}
	// 非致命警告与data共存（GraphQL部分错误语义）
	r.Errors, r.Data = warnings, result
	return r
}

// fetch 执行计划：单条SQL返回单行单列的__root JSON原始字节。
// ID加解密的出参编码挂在此唯一出口：直通响应、resolver解包、订阅推送、
// 根Resolver回查全部经此取数，下游看到的字节里ID已是shortId
func (my *Executor) fetch(ctx context.Context, plan *Plan, variables map[string]interface{}) ([]byte, error) {
	args, err := plan.ResolveArgs(variables, scopeWithRole(ctx, scopeValues(ctx), plan.roleNeeded)) // 行级作用域值从请求上下文取
	if err != nil {
		return nil, err
	}
	database := my.database
	if tx := Tx(ctx); tx != nil {
		database = tx
	}
	var data []byte
	err = database.WithContext(ctx).Raw(plan.SQL, args...).Row().Scan(&data)
	if err == nil {
		data = encodeBytes(data, plan.paths) // 空路径树零成本短路
	}
	return data, err
}

// unpack 解包__root JSON为data（顶层key即字段别名）并执行后处理；
// 只解码resolver绑定路径覆盖的分支（宿主对象全量），路径外分支保留原始字节，
// 序列化期直通输出——免解码、免数字收敛、免重序列化。
// 第二返回值为非致命警告（如远程取数失败），随响应errors返回但不影响data
func (my *Executor) unpack(ctx context.Context, plan *Plan, data []byte, variables map[string]interface{}) (map[string]interface{}, gqlerror.List, error) {
	var result map[string]interface{}
	if len(data) == 0 {
		result = map[string]interface{}{}
	} else if v, err := splitDecode(data, pathTrie(plan.resolvers)); err != nil {
		return nil, nil, err
	} else {
		result, _ = v.(map[string]interface{})
	}
	warnings, err := my.resolve(ctx, plan.resolvers, result, variables)
	return result, warnings, err
}

// decodeTrie 绑定路径前缀树：命中键继续下行，nil子节点表示宿主分支须全量解码，
// 未命中键保留原始字节（RawMessage）直通序列化
type decodeTrie map[string]decodeTrie

// pathTrie 由绑定路径构建解码前缀树；路径末段为宿主（整对象是resolver的source）
func pathTrie(bindings []binding) decodeTrie {
	root := decodeTrie{}
	for _, b := range bindings {
		node := root
		for i, seg := range b.Path {
			child, ok := node[seg]
			if ok && child == nil {
				break // 祖先已是宿主全量解码，天然覆盖
			}
			if i == len(b.Path)-1 {
				node[seg] = nil
				break
			}
			if child == nil {
				child = decodeTrie{}
				node[seg] = child
			}
			node = child
		}
	}
	return root
}

// splitDecode 按前缀树混合解码：trie为nil全量解码并收敛数字；对象命中键携子树下行、
// 未命中键零解析保留原始字节段（零拷贝子切片，序列化期直通）；数组透明展开
// （元素共享同一trie节点，与hosts语义对齐）；路径中途遇标量（如null）原样保留
func splitDecode(raw stdjson.RawMessage, trie decodeTrie) (interface{}, error) {
	if trie == nil {
		return decodeValue(raw)
	}
	s := codecScanner{src: raw}
	s.space()
	if !s.peek('{') && !s.peek('[') {
		return raw, nil
	}
	kind, end := s.src[s.pos], byte('}')
	var obj map[string]interface{}
	var arr []interface{}
	if kind == '{' {
		obj = make(map[string]interface{})
	} else {
		end = ']'
	}
	var err error
	s.list(end, func() {
		var key []byte
		if kind == '{' {
			key = s.str()
			s.space()
			if !s.peek(':') {
				s.bad = true
				return
			}
			s.pos++
		}
		s.space()
		stop, ok := rawSpan(s.src, s.pos)
		if !ok {
			s.bad = true
			return
		}
		val := s.src[s.pos:stop]
		s.pos = stop
		if kind == '[' {
			var v interface{}
			if v, err = splitDecode(val, trie); err == nil {
				arr = append(arr, v)
			}
		} else if child, hit := trie[string(key)]; hit { // []byte键查map免分配
			obj[string(key)], err = splitDecode(val, child)
		} else {
			obj[string(key)] = stdjson.RawMessage(val)
		}
		if err != nil {
			s.bad = true // 终止扫描，错误经err外传
		}
	})
	if err != nil {
		return nil, err
	}
	s.space()
	if s.bad || s.pos != len(s.src) {
		return nil, fmt.Errorf("解包失败：结果字节非合法JSON")
	}
	if kind == '[' {
		return arr, nil
	}
	return obj, nil
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
// 调用方（run）路由到自省投影，其余入口（订阅/持久化预热）按错误处理
type introQuery struct {
	operation *ast.OperationDefinition
}

func (my *introQuery) Error() string { return "自省查询不支持此入口" }

// plan 获取执行计划：普通查询命中缓存零解析；volatile命中仅重做SQL构建；
// 冷查询每个请求独立校验变量，编译经singleflight收敛；自省经introQuery带出。
func (my *Executor) plan(query, operationName string, variables map[string]interface{}) (*Plan, error) {
	// Directive results are part of the selection shape. Evaluate them before
	// lookup and partition the cache by that shape so true/false cannot alias.
	if hasConditionalDirective(query) {
		operation, err := my.parse(query, operationName, variables)
		if err != nil {
			return nil, err
		}
		key := planKey{operation: operationName, query: query, shape: selectionShape(operation.SelectionSet)}
		if entry, ok := my.cache.Get(key); ok {
			return my.preparedPlan(entry, variables)
		}
		entry, plan, err := my.miss(key, operation, variables)
		if err != nil || plan != nil {
			return plan, err
		}
		return my.preparedPlan(entry, variables)
	}
	key := planKey{operation: operationName, query: query}
	entry, ok := my.cache.Get(key)
	if !ok {
		flightKey := operationName + "\x00" + query
		var lead *Plan // 仅领跑者闭包置位：变量已解码、编译产物直接执行，无二次开销
		leader, firstRan := false, false
		value, err, _ := my.flight.Do(flightKey, func() (interface{}, error) {
			firstRan = true
			attempt := variables
			if strings.IndexByte(query, '$') >= 0 {
				attempt = cloneVariables(variables) // 失败领跑者不污染自身重试入参
			}
			entry, plan, err := my.miss(key, nil, attempt)
			lead = plan
			leader = err == nil
			if leader {
				assignVariables(variables, attempt)
			} else if _, introspection := err.(*introQuery); introspection {
				assignVariables(variables, attempt) // 传递自省变量默认值
			}
			return entry, err
		})
		if err != nil {
			if introspection, ok := err.(*introQuery); ok {
				if !firstRan {
					if _, ownErr := coerceVariables(my.schema, introspection.operation, variables); ownErr != nil {
						return nil, ownErr
					}
				}
				return nil, err // 共享已验证AST，仅运行当前请求coercion
			}
			// 请求值错误不得从领跑者传播；各请求先校验自身，
			// 通过者再以第二个flight收敛编译。正常冷查询仍只解析一次。
			operation, ownErr := my.parse(query, operationName, variables)
			if ownErr != nil {
				return nil, ownErr
			}
			retryAttempt := cloneVariables(variables)
			value, err, _ = my.flight.Do("retry\x00"+flightKey, func() (interface{}, error) {
				entry, plan, compileErr := my.miss(key, operation, retryAttempt)
				lead = plan
				if compileErr == nil {
					assignVariables(variables, retryAttempt)
				}
				return entry, compileErr
			})
			if err != nil {
				if _, introspection := err.(*introQuery); introspection {
					return nil, err // 当前请求已校验，可安全路由自省
				}
				// cursor等volatile编译错误仍可能与值相关，最后用
				// 当前请求独立确认，不返回其他请求的错误。
				finalAttempt := cloneVariables(variables)
				entry, lead, err = my.miss(key, operation, finalAttempt)
				if err != nil {
					return nil, err
				}
				assignVariables(variables, finalAttempt)
			}
			leader = true // 当前请求已在重试前完成coercion
		}
		if lead != nil {
			return lead, nil
		}
		if entry == nil {
			entry = value.(*planEntry)
		}
		if leader {
			return my.preparedPlan(entry, variables)
		}
	}
	return my.cachedPlan(entry, variables)
}

func (my *Executor) cachedPlan(entry *planEntry, variables map[string]interface{}) (*Plan, error) {
	// 收口：任何消费（Resolver分发/执行/volatile重编译）前统一做变量
	// coercion，再还原codec入参。缓存命中也必须先过请求级校验。
	if _, err := coerceVariables(my.schema, entry.operation, variables); err != nil {
		return nil, err
	}
	return my.preparedPlan(entry, variables)
}

func (my *Executor) preparedPlan(entry *planEntry, variables map[string]interface{}) (*Plan, error) {
	decodeVariables(my.schema, entry.operation.VariableDefinitions, variables, my.metadata)
	if entry.rootResolver {
		return nil, &rootResolverQuery{entry: entry}
	}
	if entry.plan != nil {
		return entry.plan, nil
	}
	// volatile：仅重做SQL构建，binding与codec路径树复用缓存（均只依赖AST）
	plan, err := my.compiler.compile(entry.operation, variables, false)
	if err != nil {
		return nil, err
	}
	plan.resolvers, plan.paths = entry.resolvers, entry.paths
	plan.intro = entry.intro
	plan.roleNeeded = entry.roleNeeded
	plan.policyNeeded = entry.policyNeeded
	return plan, nil
}

// miss 缓存未命中的singleflight领跑者：落缓存条目并带回本次
// 编译计划；重试路径可传入已校验operation，正常路径在flight内解析。
func (my *Executor) miss(key planKey, operation *ast.OperationDefinition, variables map[string]interface{}) (*planEntry, *Plan, error) {
	if operation == nil {
		var err error
		operation, err = my.parse(key.query, key.operation, variables)
		if err != nil {
			return nil, nil, err
		}
	}
	operation, introspection := splitIntrospection(operation)
	if introspection != nil {
		if !my.options().Introspection {
			return nil, nil, fmt.Errorf("自省查询已关闭（schema.introspection=false）")
		}
		if operation == nil {
			return nil, nil, &introQuery{operation: introspection}
		}
	}
	if len(operation.SelectionSet) == 0 || my.needsRootExecution(operation) {
		roleNeeded, policyNeeded := operationGovernanceNeeds(my.metadata, operation)
		entry := &planEntry{
			operation:    operation,
			intro:        introspection,
			rootResolver: true,
			roleNeeded:   roleNeeded,
			policyNeeded: policyNeeded,
		}
		my.cache.Put(key, entry)
		return entry, nil, nil
	}
	if my.compiler == nil || my.database == nil {
		return nil, nil, fmt.Errorf("执行器未配置数据库或编译器")
	}
	// 领跑者在此解码入参并编译（跟随者在命中路径自行解码），两侧各解码一次
	decodeVariables(my.schema, operation.VariableDefinitions, variables, my.metadata)
	return my.compile(key, operation, introspection, variables)
}

// parse 解析校验查询并选定操作，fragment就地展开为纯字段选择集
func (my *Executor) parse(query, operationName string, variables map[string]interface{}) (*ast.OperationDefinition, error) {
	doc, errs := gqlparser.LoadQueryWithRules(my.schema, query, nil)
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
	coerced, err := coerceVariables(my.schema, operation, variables)
	if err != nil {
		return nil, err
	}
	variables = coerced
	selection, err := normalizeSelectionWithSchema(operation.SelectionSet, doc.Fragments, variables, my.schema)
	if err != nil {
		return nil, err
	}
	operation.SelectionSet = selection
	// 统一前端深度护栏：在路由到 SQL、根 Resolver 或自省 Handler 前拒绝
	// 超深选择集；Compiler 内的同一检查作为 defense-in-depth 保留。
	if err := my.validateMaxDepth(operation); err != nil {
		return nil, err
	}
	// 字段参数的codec字面量解析期一次还原；变量默认值保持
	// 原始GraphQL形态，每个请求物化后由decodeVariables仅转换一次。
	decodeLiterals(my.schema, operation.SelectionSet, my.metadata)
	return operation, nil
}

func (my *Executor) validateMaxDepth(operation *ast.OperationDefinition) error {
	if operation == nil || my.metadata == nil {
		return nil
	}
	if max := my.metadata.MaxDepth(); max > 0 {
		if depth := depthOf(operation.SelectionSet); depth > max {
			return fmt.Errorf("查询嵌套深度%d超过上限%d（schema.max-depth可调，0=不限制）", depth, max)
		}
	}
	return nil
}

// compile 编译并缓存：resolver绑定与codec路径树在此一次性收集（均只依赖AST）；
// volatile计划SQL不可复用但AST与两类产物可以——缓存供后续请求免解析重收集
func (my *Executor) compile(key planKey, operation, introspection *ast.OperationDefinition, variables map[string]interface{}) (*planEntry, *Plan, error) {
	plan, err := my.compiler.Compile(operation, variables)
	if err != nil {
		return nil, nil, err
	}
	plan.resolvers = collectBindings(my.metadata, operation)
	plan.paths = collectCodecPaths(operation.SelectionSet, my.metadata)
	plan.intro = introspection
	entry := &planEntry{operation: operation, intro: introspection, resolvers: plan.resolvers, paths: plan.paths, roleNeeded: plan.roleNeeded, policyNeeded: plan.policyNeeded}
	if !plan.Volatile() {
		entry.plan = plan
	}
	my.cache.Put(key, entry)
	return entry, plan, nil
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

// splitIntrospection separates built-in introspection projection from executable
// business fields while preserving one validated operation and variable contract.
func splitIntrospection(operation *ast.OperationDefinition) (*ast.OperationDefinition, *ast.OperationDefinition) {
	if operation == nil || !hasIntroField(operation.SelectionSet) {
		return operation, nil
	}
	data, introspection := make(ast.SelectionSet, 0, len(operation.SelectionSet)), make(ast.SelectionSet, 0, 2)
	for _, selection := range operation.SelectionSet {
		field, _ := selection.(*ast.Field)
		if field != nil && (field.Name == "__schema" || field.Name == "__type") {
			introspection = append(introspection, field)
		} else {
			data = append(data, selection)
		}
	}
	introOperation := *operation
	introOperation.SelectionSet = introspection
	if len(data) == 0 {
		return nil, &introOperation
	}
	dataOperation := *operation
	dataOperation.SelectionSet = data
	return &dataOperation, &introOperation
}

func (my *Executor) mergeIntrospection(data map[string]interface{}, operation *ast.OperationDefinition, variables map[string]interface{}) error {
	projected, err := my.intro.Introspect(operation, variables)
	if err != nil {
		return err
	}
	for key, value := range projected {
		data[key] = value
	}
	return nil
}
