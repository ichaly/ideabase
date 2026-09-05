package gql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/ichaly/ideabase/gql/internal/intro"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/log"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"golang.org/x/sync/errgroup"
)

var errRootTransactionRollback = errors.New("root resolver transaction rollback")

// Resolver 自定义字段解析器：处理无法用SQL表达的字段逻辑
// 元数据中通过 Field.Resolver 按名绑定，编译期跳过SQL，执行期填充结果
// 列表场景下Resolve会被并发调用，实现须线程安全；有状态逻辑请实现BatchResolver
type Resolver interface {
	Name() string
	Define() Define
	// Resolve 计算单个宿主对象的字段值，source为该对象已查出的字段
	Resolve(ctx context.Context, source any, args map[string]interface{}) (interface{}, error)
}

// Handler 是所有字段执行器共享的声明契约；Resolver是开发者面对的主API，
// SQL/Batch/Remote只是在同一契约下采用不同快路径。
type Handler interface {
	Name() string
	Define() Define
}

// NextResolver 是泛型Resolver中间件调用下游的强类型函数。
type NextResolver[S, I, O any] func(context.Context, S, I) (O, error)

// ResolverMiddleware 包装一个已注册Resolver；类型擦除只存在于注册表边界。
type ResolverMiddleware func(Resolver) Resolver

// NewResolverMiddleware 创建强类型Resolver中间件，可修改source/args并选择是否调用next。
func NewResolverMiddleware[S, I, O any](
	fn func(context.Context, S, I, NextResolver[S, I, O]) (O, error),
) ResolverMiddleware {
	return func(next Resolver) Resolver {
		return &resolverMiddleware[S, I, O]{next: next, fn: fn}
	}
}

type resolverMiddleware[S, I, O any] struct {
	next Resolver
	fn   func(context.Context, S, I, NextResolver[S, I, O]) (O, error)
}

type typedResolver[S, I, O any] interface {
	resolveTyped(context.Context, S, I) (O, error)
}

func (my *resolverMiddleware[S, I, O]) Name() string   { return my.next.Name() }
func (my *resolverMiddleware[S, I, O]) Define() Define { return my.next.Define() }
func (my *resolverMiddleware[S, I, O]) Resolve(ctx context.Context, raw any, args map[string]interface{}) (interface{}, error) {
	source, ok := raw.(S)
	if !ok {
		return nil, fmt.Errorf("resolver %s source类型不匹配: %T", my.Name(), raw)
	}
	input, err := decode[I](args)
	if err != nil {
		return nil, err
	}
	out, err := my.resolveTyped(ctx, source, input)
	if err != nil {
		return nil, err
	}
	return erase(out), nil
}

func (my *resolverMiddleware[S, I, O]) resolveTyped(ctx context.Context, source S, input I) (O, error) {
	next := func(ctx context.Context, source S, input I) (O, error) {
		if typed, ok := my.next.(typedResolver[S, I, O]); ok {
			return typed.resolveTyped(ctx, source, input)
		}
		var zero O
		mapped, _ := plain(input).(map[string]interface{})
		value, err := my.next.Resolve(ctx, source, mapped)
		if err != nil || value == nil {
			return zero, err
		}
		out, ok := value.(O)
		if !ok {
			return zero, fmt.Errorf("resolver %s 返回类型不匹配: %T", my.Name(), value)
		}
		return out, nil
	}
	return my.fn(ctx, source, input, next)
}

// BatchResolver 批量解析器：列表场景一次调用处理全部宿主对象，避免N+1
// 返回值必须与sources等长且一一对应
type BatchResolver interface {
	Resolver
	ResolveBatch(ctx context.Context, sources []map[string]interface{}, args map[string]interface{}) ([]interface{}, error)
}

// binding 编译期收集的后处理绑定：宿主对象路径 + 目标字段 + 处理器名。
// Key非空即远程关系绑定（Name为数据源名，Key为补投影的内部键别名），否则为resolver绑定
type binding struct {
	Path  []string     // data根到宿主对象的字段别名路径（数组层级在执行期透明展开）
	Field string       // 要填充的字段别名
	Name  string       // resolver名或远程数据源名
	Key   string       // 远程绑定的宿主键别名（编译期补投影的内部列）
	Strip []string     // planner内部补投影、全部Handler完成后从响应剥除的字段
	node  *ast.Field   // resolver字段AST引用：执行期按请求变量解出实参（随计划缓存复用）
	trail []*ast.Field // Path每一段对应的字段，用于错误冷路径按nullability冒泡
}

// collectBindings 遍历操作选择集，收集所有resolver字段的绑定
func collectBindings(meta *Metadata, operation *ast.OperationDefinition) []binding {
	var bindings []binding

	var walk func(class *protocol.Class, set ast.SelectionSet, path []string, trail []*ast.Field)
	walk = func(class *protocol.Class, set ast.SelectionSet, path []string, trail []*ast.Field) {
		selected := make(map[string]bool, len(set))
		for _, selection := range set {
			if field, ok := selection.(*ast.Field); ok && field.Alias == field.Name {
				selected[field.Name] = true
			}
		}
		for _, s := range set {
			f, ok := s.(*ast.Field)
			if !ok {
				continue
			}
			if f.Name == protocol.ITEMS {
				walk(class, f.SelectionSet, append(path[:len(path):len(path)], f.Alias), appendField(trail, f))
				continue
			}
			if f.Name == "edges" || f.Name == "node" {
				walk(class, f.SelectionSet, append(path[:len(path):len(path)], f.Alias), appendField(trail, f))
				continue
			}
			field, ok := class.Fields[f.Name]
			if !ok {
				continue
			}
			if field.Resolver != "" {
				strip := make([]string, 0, len(field.Requires))
				for _, dependency := range field.Requires {
					if !selected[dependency] {
						strip = append(strip, dependency)
					}
				}
				bindings = append(bindings, binding{
					Path:  path,
					Field: f.Alias,
					Name:  field.Resolver,
					Strip: strip,
					node:  f,
					trail: trail,
				})
				continue
			}
			if field.Remote != nil {
				bindings = append(bindings, binding{
					Path:  path,
					Field: f.Alias,
					Name:  field.Remote.Source,
					Key:   remoteKeyAlias(field.Remote.Key),
					node:  f,
					trail: trail,
				})
				continue
			}
			if field.Column == "" && field.Relation != nil {
				if target, ok := meta.GetNode(field.Relation.TargetClass); ok {
					walk(target, f.SelectionSet, append(path[:len(path):len(path)], f.Alias), appendField(trail, f))
				}
			}
		}
	}

	for _, s := range operation.SelectionSet {
		f, ok := s.(*ast.Field)
		if !ok {
			continue
		}
		// 变更读回直接是实体类型，查询根字段类型为XxxResult包装：
		// 先按原名匹配（本名以Result结尾的实体如ExamResult不可误剪），miss再剪后缀
		className := f.Definition.Type.Name()
		class, ok := meta.GetNode(className)
		if !ok {
			class, ok = meta.GetNode(strings.TrimSuffix(className, protocol.SUFFIX_RESULT))
		}
		if !ok {
			class, ok = meta.GetNode(strings.TrimSuffix(className, protocol.SUFFIX_CONNECTION))
		}
		if ok {
			walk(class, f.SelectionSet, []string{f.Alias}, []*ast.Field{f})
		}
	}
	return bindings
}

func appendField(fields []*ast.Field, field *ast.Field) []*ast.Field {
	return append(fields[:len(fields):len(fields)], field)
}

// hosts 按路径收集宿主对象，数组层级自动展开
// 单遍直收map，无interface{}装箱中间层；每段新建next切片
// （不可复用cur底层数组：数组段展开后宿主数可超上层，原地append会覆写未读元素）
func hosts(root map[string]interface{}, path []string) []map[string]interface{} {
	cur := []map[string]interface{}{root}
	for _, segment := range path {
		var next []map[string]interface{}
		for _, node := range cur {
			switch value := node[segment].(type) {
			case []interface{}:
				for _, item := range value {
					if object, ok := item.(map[string]interface{}); ok {
						next = append(next, object)
					}
				}
			case map[string]interface{}:
				next = append(next, value)
			}
		}
		cur = next
	}
	return cur
}

type hostRef struct {
	value      map[string]interface{}
	path       ast.Path
	boundaries []nullBoundary
}

type nullBoundary struct {
	typeRef *ast.Type
	setNull func()
}

type nonNullResolverError struct{ errors gqlerror.List }

func (e *nonNullResolverError) Error() string { return e.errors.Error() }
func (e *nonNullResolverError) Unwrap() error { return e.errors }

func hostRefs(root map[string]interface{}, binding binding) []hostRef {
	refs := []hostRef{{value: root}}
	for depth, segment := range binding.Path {
		next := make([]hostRef, 0, len(refs))
		for _, ref := range refs {
			var fieldType *ast.Type
			if depth < len(binding.trail) && binding.trail[depth].Definition != nil {
				fieldType = binding.trail[depth].Definition.Type
			}
			parent := ref.value
			fieldBoundary := nullBoundary{typeRef: fieldType, setNull: func() { parent[segment] = nil }}
			switch value := ref.value[segment].(type) {
			case []interface{}:
				for i, item := range value {
					if object, ok := item.(map[string]interface{}); ok {
						index := i
						var elementType *ast.Type
						if fieldType != nil {
							elementType = fieldType.Elem
						}
						boundaries := appendBoundary(ref.boundaries, fieldBoundary)
						boundaries = appendBoundary(boundaries, nullBoundary{typeRef: elementType, setNull: func() { value[index] = nil }})
						next = append(next, hostRef{value: object, path: appendPath(ref.path, segment, ast.PathIndex(i)), boundaries: boundaries})
					}
				}
			case map[string]interface{}:
				next = append(next, hostRef{value: value, path: appendPath(ref.path, segment), boundaries: appendBoundary(ref.boundaries, fieldBoundary)})
			}
		}
		refs = next
	}
	return refs
}

func appendBoundary(boundaries []nullBoundary, boundary nullBoundary) []nullBoundary {
	return append(boundaries[:len(boundaries):len(boundaries)], boundary)
}

// completeResolverFailure nulls the failed field, then follows GraphQL's
// Non-Null chain through object, list element, list and root boundaries.
func completeResolverFailure(ref hostRef, binding binding) bool {
	ref.value[binding.Field] = nil
	if binding.node == nil || binding.node.Definition == nil || binding.node.Definition.Type == nil || !binding.node.Definition.Type.NonNull {
		return false
	}
	for i := len(ref.boundaries) - 1; i >= 0; i-- {
		boundary := ref.boundaries[i]
		boundary.setNull()
		if boundary.typeRef == nil || !boundary.typeRef.NonNull {
			return false
		}
	}
	return true
}

func appendPath(path ast.Path, segment string, index ...ast.PathElement) ast.Path {
	out := append(ast.Path(nil), path...)
	out = append(out, ast.PathName(segment))
	return append(out, index...)
}

// resolve 按绑定填充后处理字段。远程关系绑定先行：网络取数并发（各job独立）、
// 回填串行（宿主map非并发安全），失败字段置null并以警告随响应errors返回（不中断）；
// resolver绑定随后：批量解析器整列表一次调用，普通解析器逐宿主并行计算
func (my *Engine) resolve(ctx context.Context, bindings []binding, data map[string]interface{}, variables map[string]interface{}) (gqlerror.List, error) {
	var warnings gqlerror.List
	var jobs []*remoteJob
	type batchResult struct {
		values []interface{}
		err    error
	}
	batchMemo := make(map[string]batchResult)
	for _, b := range bindings {
		if b.Key == "" {
			continue
		}
		if sources := hosts(data, b.Path); len(sources) > 0 {
			jobs = append(jobs, &remoteJob{binding: b, sources: sources, executor: my})
		}
	}
	if len(jobs) > 0 {
		rootNull := false
		group, gctx := errgroup.WithContext(ctx)
		group.SetLimit(8)
		for _, job := range jobs {
			group.Go(func() error { job.fetch(gctx, my.remotes); return nil })
		}
		_ = group.Wait() // job错误不经group传播(容错语义),此处仅同步
		for _, job := range jobs {
			job.fill()
			if job.err != nil {
				refs := hostRefs(data, job.binding)
				if len(refs) == 0 {
					warnings = append(warnings, resolverFieldErrors(job.binding, nil, job.err)...)
				} else {
					for _, ref := range refs {
						warnings = append(warnings, resolverFieldErrors(job.binding, ref.path, job.err)...)
						rootNull = completeResolverFailure(ref, job.binding) || rootNull
					}
				}
			}
		}
		for _, job := range jobs { // 全部回填后再剥内部键：多个远程可共用同一宿主键列
			job.strip()
		}
		if rootNull {
			return warnings, &nonNullResolverError{errors: warnings}
		}
	}

	for _, b := range bindings {
		if b.Key != "" {
			continue
		}
		sources := hosts(data, b.Path)
		if len(sources) == 0 {
			continue
		}
		resolver, ok := my.resolvers[b.Name]
		if !ok {
			return warnings, fmt.Errorf("resolver未注册: %s", b.Name)
		}
		var args map[string]interface{}
		if b.node != nil && len(b.node.Arguments) > 0 {
			args = b.node.ArgumentMap(variables)
		}

		var values []interface{}
		var err error
		var itemErrors []error
		callCtx := my.executionContextFor(ctx, b.node.SelectionSet, b.Name)
		if many, ok := resolver.(BatchResolver); ok {
			key := batchRequestKey(b, args)
			if cached, exists := batchMemo[key]; exists {
				values, err = cached.values, cached.err
			} else if len(sources) > handlerBatchLimit(resolver) {
				err = fmt.Errorf("batch超过限制: %d", handlerBatchLimit(resolver))
			} else {
				hctx, cancel := handlerContext(callCtx, resolver)
				err = safely(func() (err error) {
					values, err = many.ResolveBatch(hctx, sources, args)
					return handlerResult(hctx, err)
				})
				cancel()
			}
			batchMemo[key] = batchResult{values: values, err: err}
			if err == nil && len(values) != len(sources) {
				err = fmt.Errorf("返回数量不匹配: 期望%d实际%d", len(sources), len(values))
			}
		} else {
			values, itemErrors = resolveEachPartial(callCtx, resolver, sources, args)
		}
		if err != nil {
			refs := hostRefs(data, b)
			rootNull := false
			for i := range refs {
				warnings = append(warnings, resolverFieldErrors(b, refs[i].path, err)...)
				rootNull = completeResolverFailure(refs[i], b) || rootNull
			}
			if rootNull {
				return warnings, &nonNullResolverError{errors: warnings}
			}
			continue
		}
		var refs []hostRef
		for i, source := range sources {
			if i < len(itemErrors) && itemErrors[i] != nil {
				if refs == nil {
					refs = hostRefs(data, b)
				}
				warnings = append(warnings, resolverFieldErrors(b, refs[i].path, itemErrors[i])...)
				if completeResolverFailure(refs[i], b) {
					return warnings, &nonNullResolverError{errors: warnings}
				}
			} else {
				source[b.Field] = values[i]
			}
		}
	}
	for _, b := range bindings {
		if b.Key != "" || len(b.Strip) == 0 {
			continue
		}
		for _, source := range hosts(data, b.Path) {
			for _, field := range b.Strip {
				delete(source, field)
			}
		}
	}
	return warnings, nil
}

func batchRequestKey(b binding, args map[string]interface{}) string {
	encoded, _ := jsonNumeric.Marshal(args)
	var selection strings.Builder
	writeSelectionSet(&selection, b.node.SelectionSet)
	return b.Name + "\x00" + strings.Join(b.Path, "\x00") + "\x00" + string(encoded) + "\x00" + selection.String()
}

func resolverFieldErrors(b binding, hostPath ast.Path, err error) gqlerror.List {
	path := append(append(ast.Path(nil), hostPath...), ast.PathName(b.Field))
	originals := GraphQLErrors(resolverFailure(err))
	if len(originals) == 0 {
		return gqlerror.List{gqlerror.ErrorPathf(path, "resolver failed")}
	}
	result := make(gqlerror.List, len(originals))
	for i, original := range originals {
		copy := *original
		copy.Path = append(path, original.Path...)
		if len(copy.Locations) == 0 && b.node != nil && b.node.Position != nil {
			copy.Locations = []gqlerror.Location{{Line: b.node.Position.Line, Column: b.node.Position.Column}}
		}
		extensions := make(map[string]any, len(copy.Extensions)+1)
		for key, value := range copy.Extensions {
			extensions[key] = value
		}
		if current, ok := extensions["code"]; !ok || current == "EXECUTION_ERROR" {
			extensions["code"] = "RESOLVER_ERROR"
		}
		copy.Extensions = extensions
		result[i] = &copy
	}
	return result
}

// resolveEach 普通resolver逐宿主有界并发计算，返回与sources对位的结果
func resolveEachPartial(ctx context.Context, resolver Resolver, sources []map[string]interface{}, args map[string]interface{}) ([]interface{}, []error) {
	values := make([]interface{}, len(sources))
	errs := make([]error, len(sources))
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(8)
	for i, source := range sources {
		i, source := i, source
		group.Go(func() error {
			hctx, cancel := handlerContext(ctx, resolver)
			defer cancel()
			errs[i] = safely(func() (err error) {
				values[i], err = resolver.Resolve(hctx, source, args)
				return handlerResult(hctx, err)
			})
			return nil
		})
	}
	_ = group.Wait()
	return values, errs
}

// safely 拦截用户实现（resolver/remote）的panic转为错误：
// errgroup不recover，goroutine里的panic会打崩整个服务进程，引擎边界必须兜底
func safely(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().Str("panic", fmt.Sprint(r)).Bytes("stack", debug.Stack()).Msg("resolver or remote handler panicked")
			e := gqlerror.Errorf("resolver failed")
			e.Extensions = map[string]any{"code": "RESOLVER_ERROR"}
			err = e
		}
	}()
	return fn()
}

func safelyCall[T any](fn func() (T, error)) (value T, err error) {
	err = safely(func() error {
		value, err = fn()
		return err
	})
	return value, err
}

// Define 是Resolver/Remote注册即声明的schema形状。
type Define struct {
	Class          string   // Query、Mutation或宿主实体名
	Name           string   // 字段名，如 botSave、greeting
	Doc            string   // 字段描述，渲染为SDL文档字符串
	Args           string   // 参数签名原文，如 "nickname: String!, avatar: String"；空=无参
	Result         string   // 返回类型，如 BotProfile、Int
	Extra          string   // 附加SDL（辅助input/type等声明），重建时按文本去重并入schema
	Cost           uint32   // 单次字段解析权重；0使用默认权重1
	Existing       bool     // 绑定schema已有字段；仅允许通过显式Replace注册
	Resource       string   // PolicyProvider治理的资源；自定义根字段须显式声明
	Actions        []string // 该Resolver执行的治理动作
	PolicyAware    bool     // 自定义实现显式消费PolicyDecisionFromContext
	Requires       []string // 实体Resolver的内部宿主字段依赖
	Timeout        time.Duration
	RetrySafe      bool
	IdempotencyKey string
	MaxBatch       int
	SemanticKind   SemanticKind // custom handler business meaning; empty derives from mount location
	inputType      reflect.Type // 仅供启动期生成强类型嵌套input，不进入运行期热路径
	outputType     reflect.Type // 仅供启动期生成强类型output，不进入运行期热路径
}

// sdl 渲染声明为extend片段（文档用三引号块，内容含引号也合法；Extra由rebuild单独并入）
func (my Define) sdl() string {
	if my.Existing {
		return ""
	}
	kind, args, doc := my.Class, "", ""
	if my.Args != "" {
		args = "(" + my.Args + ")"
	}
	if my.Doc != "" {
		doc = `  """` + my.Doc + `"""` + "\n"
	}
	return fmt.Sprintf("extend type %s {\n%s  %s%s: %s\n}", kind, doc, my.Name, args, my.Result)
}

// mounted 注册即声明的字段级实现（NewResolver/NewBatch/NewRemote构造）：
// Register时把字段挂载进宿主实体元数据并重建schema
type mounted interface {
	Define() Define
	field() *protocol.Field
}

// mount 挂载声明为宿主实体的虚拟字段：绑定收集与SQL编译跳过据此判定；
// 与既有字段同名直接报错（不做静默覆盖，启动即失败）
func (my *Engine) mount(m mounted) error {
	d := m.Define()
	if d.Class == "Query" || d.Class == "Mutation" {
		return nil
	}
	class, ok := my.metadata.GetNode(d.Class)
	if !ok {
		return fmt.Errorf("宿主实体不存在: %s", d.Class)
	}
	if _, ok = class.Fields[d.Name]; ok {
		return fmt.Errorf("字段已存在: %s.%s", d.Class, d.Name)
	}
	field := m.field()
	for i, dependency := range field.Requires {
		required, exists := class.Fields[dependency]
		if !exists || required.Column == "" {
			return fmt.Errorf("resolver %s依赖未知列字段: %s", m.Define().Name, dependency)
		}
		field.Requires[i] = required.Name
	}
	field.Name, field.Type, field.Description, field.Virtual = d.Name, d.Result, d.Doc, true
	class.Fields[d.Name] = field
	return nil
}

// rebuild 合并全部注册声明重建schema（Resolver/Remote共用；仅限启动期）：
// extend片段按注册键排序保证文本稳定，附加SDL按内容去重（多个声明共享同一虚拟类型）
func (my *Engine) rebuild() error {
	defines := make(map[string]Define, len(my.resolvers)+len(my.remotes))
	for name, r := range my.resolvers {
		defines[name] = r.Define()
	}
	for name, r := range my.remotes {
		if m, ok := r.(mounted); ok {
			defines[name] = m.Define()
		}
	}

	names := sortedMapKeys(defines)

	var sb strings.Builder
	sb.WriteString(my.source)
	extras := make(map[string]bool)
	base, err := gqlparser.LoadSchema(&ast.Source{Name: "base.graphql", Input: my.source})
	if err != nil {
		return fmt.Errorf("解析基础schema失败: %w", err)
	}
	known := make(map[string]bool, len(base.Types))
	for name := range base.Types {
		known[name] = true
	}
	for _, name := range names {
		d := defines[name]
		if sdl := d.sdl(); sdl != "" {
			sb.WriteString("\n")
			sb.WriteString(sdl)
		}
		if d.Extra != "" && !extras[d.Extra] {
			extras[d.Extra] = true
			sb.WriteString("\n")
			sb.WriteString(d.Extra)
		}
		generated := generatedTypes(d, known)
		if generated != "" {
			sb.WriteString("\n")
			sb.WriteString(generated)
		}
	}
	s, err := gqlparser.LoadSchema(&ast.Source{Name: "schema.graphql", Input: sb.String()})
	if err != nil {
		return fmt.Errorf("合并注册声明失败: %w", err)
	}
	disableUnsupportedDirectives(s)
	synchronizeSubscriptionSchema(my, s)
	my.schema = s
	my.intro = intro.New(s)
	my.cache = newPlanCache(planCacheSize)
	my.policyCache = newPlanCache(planCacheSize)
	return nil
}

type transactionKey struct{}

func transactionFromContext(ctx context.Context) *sql.Tx {
	if ctx == nil {
		return nil
	}
	tx, _ := ctx.Value(transactionKey{}).(*sql.Tx)
	return tx
}

// rootResolverQuery 携带需要逐根字段协调执行的缓存条目。
type rootResolverQuery struct{ entry *planEntry }

func (my *rootResolverQuery) Error() string { return "根字段Resolver不支持此入口" }

// needsRootExecution 判断操作是否需要逐根字段协调：根Resolver和多根mutation
// 进入协调路径；单个默认CRUD仍保留原单SQL原子快路径。
func (my *Engine) needsRootExecution(operation *ast.OperationDefinition) bool {
	fields := 0
	for _, s := range operation.SelectionSet {
		f, ok := s.(*ast.Field)
		if !ok || strings.HasPrefix(f.Name, "__") {
			continue
		}
		parent := "Query"
		if f.ObjectDefinition != nil {
			parent = f.ObjectDefinition.Name
		}
		if _, ok = my.resolvers[parent+"."+f.Name]; ok {
			return true
		}
		if operation.Operation == ast.Mutation {
			fields++
			if fields > 1 {
				return true
			}
		}
	}
	return operation.Operation == ast.Mutation && fields == 0
}

func (my *Engine) executeRootResolvers(ctx context.Context, entry *planEntry, variables map[string]interface{}) gqlReply {
	ctx = withInvalidationState(ctx)
	operation, plans := entry.operation, &entry.rootPlans
	typename := "Query"
	if operation.Operation == ast.Mutation {
		typename = "Mutation"
	}
	data := make(map[string]interface{}, len(operation.SelectionSet))
	var warnings gqlerror.List
	var executionErrors gqlerror.List
	stop := false
	rollbackConfirmed := false
	execute := func(ctx context.Context) error {
		var defaults map[string]interface{}
		var defaultWarnings gqlerror.List
		var defaultErr error
		if operation.Operation == ast.Query {
			defaults, defaultWarnings, defaultErr = my.executeDefaultRootFields(ctx, operation, variables)
			warnings = append(warnings, defaultWarnings...)
		}
		for _, s := range operation.SelectionSet {
			if stop {
				break
			}
			f, ok := s.(*ast.Field)
			if !ok {
				continue
			}
			if f.Name == "__typename" {
				data[f.Alias] = typename
				continue
			}
			if operation.Operation == ast.Query && my.resolvers[typename+"."+f.Name] == nil {
				if defaultErr != nil {
					executionErrors = append(executionErrors, rootFieldExecutionErrors(f, defaultErr, "EXECUTION_ERROR", false)...)
					data[f.Alias] = nil
					if f.Definition != nil && f.Definition.Type != nil && f.Definition.Type.NonNull {
						stop = true
					}
					continue
				}
				data[f.Alias] = defaults[responseKey(f)]
				continue
			}
			value, warns, err := my.resolveRootField(ctx, operation, f, variables, typename, plans)
			if err != nil {
				code := "EXECUTION_ERROR"
				if my.resolvers[typename+"."+f.Name] != nil {
					code = "RESOLVER_ERROR"
				}
				executionErrors = append(executionErrors, rootFieldExecutionErrors(f, err, code, false)...)
				data[f.Alias] = nil
				if f.Definition != nil && f.Definition.Type != nil && f.Definition.Type.NonNull {
					stop = true
				}
				continue
			}
			warnings = append(warnings, warns...)
			data[f.Alias] = value
		}
		return nil
	}
	var err error
	if operation.Operation == ast.Mutation && transactionFromContext(ctx) != nil {
		err, rollbackConfirmed = runMutationSavepoint(ctx, transactionFromContext(ctx), func() error {
			if err := execute(ctx); len(executionErrors) > 0 {
				return errRootTransactionRollback
			} else {
				return err
			}
		})
	} else if my.database != nil && transactionFromContext(ctx) == nil && (operation.Operation == ast.Mutation || my.rls || tenantRLSRequested(ctx)) {
		err, rollbackConfirmed = runRootTransaction(ctx, my.database, my.cdc, func(tx *sql.Tx) error {
			if my.rls || tenantRLSRequested(ctx) {
				if err := my.prepareRLS(ctx, tx); err != nil {
					return err
				}
			}
			executionCtx := context.WithValue(ctx, transactionKey{}, tx)
			if operation.Operation == ast.Mutation {
				executionCtx = context.WithValue(executionCtx, engineMutationTransactionKey{}, true)
			}
			if my.rls || tenantRLSRequested(ctx) {
				executionCtx = markRLSPrepared(executionCtx, tx)
			}
			err := execute(executionCtx)
			if len(executionErrors) > 0 {
				return errRootTransactionRollback
			}
			return err
		})
	} else {
		err = execute(ctx)
	}
	if err != nil && len(executionErrors) == 0 {
		reply := gqlReply{Errors: GraphQLErrors(err)}
		if operation.Operation == ast.Mutation {
			state := "indeterminate"
			if rollbackConfirmed {
				state = "rolled_back"
			}
			return reply.mutationCommit(ctx, state)
		}
		return reply
	}
	if len(executionErrors) > 0 {
		state := "indeterminate"
		if rollbackConfirmed && operation.Operation == ast.Mutation {
			state = "rolled_back"
			markTransactionRolledBack(executionErrors)
			return gqlReply{Errors: append(executionErrors, warnings...), nullData: true}.mutationCommit(ctx, state)
		}
		if stop {
			return gqlReply{Errors: executionErrors, nullData: true}.mutationCommit(ctx, state)
		}
		return gqlReply{Data: data, Errors: append(executionErrors, warnings...)}.mutationCommit(ctx, state)
	}
	reply := gqlReply{Data: data, Errors: warnings}
	if operation.Operation == ast.Mutation {
		return reply.mutationCommit(ctx, "committed")
	}
	return reply
}

func (my *Engine) executeDefaultRootFields(ctx context.Context, operation *ast.OperationDefinition, variables map[string]interface{}) (map[string]interface{}, gqlerror.List, error) {
	selection := make(ast.SelectionSet, 0, len(operation.SelectionSet))
	for _, node := range operation.SelectionSet {
		field, ok := node.(*ast.Field)
		if ok && field.Name != "__typename" && my.resolvers["Query."+field.Name] == nil {
			selection = append(selection, field)
		}
	}
	if len(selection) == 0 {
		return nil, nil, nil
	}
	if my.compiler == nil {
		return nil, nil, fmt.Errorf("gql compiler is unavailable")
	}
	sub := *operation
	sub.SelectionSet = selection
	plan, err := my.compiler.Compile(&sub, variables)
	if err != nil {
		return nil, nil, err
	}
	if my.policy != nil {
		plan, err = my.policyPlan(ctx, plan, variables)
		if err != nil {
			return nil, nil, err
		}
	}
	done := my.startPhase(ctx, PhaseDatabase)
	raw, err := my.fetch(ctx, plan, variables)
	if done != nil {
		done(observationErrorCode(err))
	}
	if err != nil {
		return nil, nil, err
	}
	var root map[string]interface{}
	var warnings gqlerror.List
	if len(plan.resolvers) == 0 {
		value, decodeErr := decodeValue(raw)
		if decodeErr != nil {
			return nil, nil, decodeErr
		}
		root, _ = value.(map[string]interface{})
	} else {
		root, warnings, err = my.unpack(ctx, plan, raw, variables)
		if err != nil {
			return nil, warnings, err
		}
	}
	return root, warnings, nil
}

func runRootTransaction(ctx context.Context, database *Database, publisher interface{ invalidate(...string) }, execute func(*sql.Tx) error) (error, bool) {
	tx, err := database.Pool().BeginTx(ctx, nil)
	if err != nil {
		return err, false
	}
	if err := execute(tx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("mutation rollback failed: %w", rollbackErr)), false
		}
		return err, true
	}
	if err := tx.Commit(); err != nil {
		return err, false
	}
	if publisher != nil {
		publisher.invalidate(invalidationTables(ctx)...)
	}
	return nil, false
}

// runMutationSavepoint makes a nested mutation atomic without taking ownership
// of the caller's transaction. PostgreSQL permits nested savepoints with the
// same name; RELEASE/ROLLBACK always address the innermost one.
func runMutationSavepoint(ctx context.Context, tx *sql.Tx, execute func() error) (error, bool) {
	const name = "gql_mutation"
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
		return err, false
	}
	if err := execute(); err != nil {
		if _, rollbackErr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+name); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("mutation savepoint rollback failed: %w", rollbackErr)), false
		}
		if _, releaseErr := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+name); releaseErr != nil {
			return errors.Join(err, releaseErr), true
		}
		return err, true
	}
	_, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+name)
	return err, false
}

func markTransactionRolledBack(errors gqlerror.List) {
	for _, err := range errors {
		if err.Extensions == nil {
			err.Extensions = make(map[string]any)
		}
		err.Extensions["transactionRolledBack"] = true
	}
}

func rootFieldExecutionErrors(field *ast.Field, err error, code string, rolledBack bool) gqlerror.List {
	alias := field.Alias
	if alias == "" {
		alias = field.Name
	}
	prefix := ast.Path{ast.PathName(alias)}
	if code == "RESOLVER_ERROR" {
		err = resolverFailure(err)
	}
	result := GraphQLErrors(err)
	for i, original := range result {
		copy := *original
		copy.Path = append(prefix, original.Path...)
		if len(copy.Locations) == 0 && field.Position != nil {
			copy.Locations = []gqlerror.Location{{Line: field.Position.Line, Column: field.Position.Column}}
		}
		ext := make(map[string]any, len(original.Extensions)+2)
		for k, v := range original.Extensions {
			ext[k] = v
		}
		if current, ok := ext["code"]; !ok || current == "EXECUTION_ERROR" {
			ext["code"] = code
		}
		if rolledBack {
			ext["transactionRolledBack"] = true
		}
		copy.Extensions = ext
		result[i] = &copy
	}
	return result
}

func (my *Engine) resolveRootField(ctx context.Context, operation *ast.OperationDefinition, field *ast.Field, variables map[string]interface{}, parent string, plans *sync.Map) (interface{}, gqlerror.List, error) {
	resolver := my.resolvers[parent+"."+field.Name]
	if resolver == nil {
		return my.executeDefaultRootField(ctx, operation, field, variables, plans)
	}
	var result interface{}
	err := safely(func() (err error) {
		hctx, cancel := handlerContext(my.executionContextFor(ctx, field.SelectionSet, parent+"."+field.Name), resolver)
		defer cancel()
		result, err = resolver.Resolve(hctx, Root{}, field.ArgumentMap(variables))
		return handlerResult(hctx, err)
	})
	if err != nil {
		return nil, nil, err
	}
	result, warnings, projected, err := my.enrich(ctx, operation, field, variables, result)
	if err != nil {
		return nil, warnings, err
	}
	// Both direct resolver values and entity requery values pass through the
	// same GraphQL completion path. The projected flag only describes how the
	// value was obtained; it must not bypass nullability or shape validation.
	parentType := "Query"
	if field.ObjectDefinition != nil && field.ObjectDefinition.Name != "" {
		parentType = field.ObjectDefinition.Name
	}
	if operation.Operation == ast.Mutation {
		parentType = "Mutation"
	}
	var resultType *ast.Type
	if field.Definition != nil {
		resultType = field.Definition.Type
	}
	if !projected {
		objectType := ""
		if resultType != nil {
			objectType = resultType.Name()
		}
		result = projectRootResult(result, field.SelectionSet, objectType)
	}
	completed, completionErrors, violation := completeValue(my.schema, result,
		resultType, field.SelectionSet, nil, warnings,
		completionOptions{mode: completionLocal, parentType: parentType, fieldName: field.Name})
	if !violation && len(completionErrors) > 0 {
		completionErrors = rootFieldExecutionErrors(field, completionErrors, "RESOLVER_ERROR", false)
	}
	warnings = append(warnings, completionErrors...)
	if violation {
		return nil, warnings, &nonNullResolverError{errors: warnings}
	}
	return completed, warnings, nil
}

// projectRootResult applies the client's selection to values returned directly
// by a root Resolver. SQL-backed and entity-requery paths already return a
// projected shape; this helper keeps map/struct/list root results consistent.
func projectRootResult(value any, set ast.SelectionSet, objectType string) any {
	if value == nil || len(set) == 0 {
		return value
	}
	return projectRootValue(plain(value), set, objectType)
}

func projectRootValue(value any, set ast.SelectionSet, objectType string) any {
	switch current := value.(type) {
	case []any:
		out := make([]any, len(current))
		for i, item := range current {
			out[i] = projectRootValue(item, set, objectType)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(set))
		if hasInlineFragment(set) {
			out["__typename"] = current["__typename"]
		}
		for _, selection := range set {
			switch node := selection.(type) {
			case *ast.InlineFragment:
				fragment, _ := projectRootValue(current, node.SelectionSet, objectType).(map[string]any)
				for key, value := range fragment {
					out[key] = value
				}
			case *ast.Field:
				key := responseKey(node)
				if node.Name == "__typename" {
					out[key] = objectType
					if actual, ok := current["__typename"].(string); ok {
						out[key] = actual
					}
					continue
				}
				child, exists := current[node.Name]
				if !exists {
					out[key] = nil
					continue
				}
				childType := objectType
				if node.Definition != nil && node.Definition.Type != nil {
					childType = node.Definition.Type.Name()
				}
				out[key] = projectRootValue(child, node.SelectionSet, childType)
			}
		}
		return out
	default:
		return value
	}
}

// executeDefaultRootField 在自定义根Resolver与默认数据库字段混排时逐字段复用原
// SQL编译/执行链；默认-only操作仍走整份operation单SQL快路径，零额外开销。
func (my *Engine) executeDefaultRootField(ctx context.Context, operation *ast.OperationDefinition, field *ast.Field, variables map[string]interface{}, plans *sync.Map) (interface{}, gqlerror.List, error) {
	if my.compiler == nil || my.database == nil {
		return nil, nil, fmt.Errorf("执行器未配置数据库或编译器")
	}
	var plan *Plan
	if cached, ok := plans.Load(field); ok {
		plan = cached.(*Plan)
	} else {
		sub := *operation
		sub.SelectionSet = ast.SelectionSet{field}
		var err error
		if plan, err = my.compiler.Compile(&sub, variables); err != nil {
			return nil, nil, err
		}
		plan.resolvers = collectBindings(my.metadata, &sub)
		plan.paths = collectCodecPaths(sub.SelectionSet, my.metadata)
		if !plan.Volatile() {
			actual, _ := plans.LoadOrStore(field, plan)
			plan = actual.(*Plan)
		}
	}
	if my.policy != nil {
		var err error
		plan, err = my.policyPlan(ctx, plan, variables)
		if err != nil {
			return nil, nil, err
		}
	}
	raw, err := my.fetch(ctx, plan, variables)
	if err != nil {
		return nil, nil, err
	}
	if len(plan.resolvers) == 0 {
		value, err := decodeValue(raw)
		if err != nil {
			return nil, nil, err
		}
		root, _ := value.(map[string]interface{})
		return root[field.Alias], nil, nil
	}
	root, warnings, err := my.unpack(ctx, plan, raw, variables)
	if err != nil {
		return nil, nil, err
	}
	return root[field.Alias], warnings, nil
}

// enrich 回查补全：根Resolver返回实体id时，以客户端选择集合成实体查询走既有
// 编译路径读回（计划缓存、字段级resolver、关系全部复用），对齐变更读回语义。
// 第二返回值为回查携带的非致命警告（data与errors共存时），随最终响应errors返回
func (my *Engine) enrich(ctx context.Context, operation *ast.OperationDefinition, f *ast.Field, variables map[string]interface{}, result interface{}) (interface{}, gqlerror.List, bool, error) {
	if result == nil || len(f.SelectionSet) == 0 {
		return result, nil, false, nil
	}
	switch result.(type) {
	case []interface{}:
		return result, nil, false, nil
	}
	if my.metadata == nil || f.Definition == nil || f.Definition.Type == nil {
		return result, nil, false, nil
	}
	className := f.Definition.Type.Name()
	class, ok := my.metadata.GetNode(className)
	if !ok {
		return result, nil, false, nil
	}
	primaryKeys := class.PrimaryKeyFields()
	composite := len(primaryKeys) > 1
	var values map[string]interface{}
	if encoded, err := jsonNumeric.Marshal(result); err == nil {
		var decoded map[string]interface{}
		if jsonNumeric.Unmarshal(encoded, &decoded) == nil {
			values = decoded
		}
	}
	if values != nil {
		complete := true
		for _, selection := range f.SelectionSet {
			field, fieldOK := selection.(*ast.Field)
			if !fieldOK || field.Name == "__typename" {
				continue
			}
			_, complete = values[field.Alias]
			if !complete {
				break
			}
		}
		if complete {
			return result, nil, false, nil
		}
		key := make(map[string]interface{}, len(primaryKeys))
		var single interface{}
		for _, name := range primaryKeys {
			field := class.Fields[name]
			graphqlName := name
			if field != nil && field.Name != "" {
				graphqlName = field.Name
			}
			value, exists := values[graphqlName]
			if !exists {
				value, exists = values[name]
			}
			if !exists {
				return result, nil, false, nil
			}
			key[graphqlName] = value
			single = value
		}
		if composite {
			result = key
		} else if len(primaryKeys) == 1 {
			result = single
		}
	} else if composite {
		return result, nil, false, nil
	}

	// 主键与选择集引用到的原变量均经变量通道传入：不内联字面量（map经json序列化
	// 会带引号键、枚举会变带引号字符串，均是非法GraphQL），且合成查询文本与实参
	// 无关，回查计划可按实体+选择集缓存复用
	used := make(map[string]bool)
	collectVariables(f.SelectionSet, used)
	pk := protocol.ID
	for used[pk] {
		pk = "_" + pk // 避开客户端同名变量
	}
	args := map[string]interface{}{pk: result}

	fieldName := queryField(className)
	var sb strings.Builder
	sb.WriteString("query ($")
	sb.WriteString(pk)
	if composite {
		sb.WriteString(": ")
		sb.WriteString(className)
		sb.WriteString(protocol.SUFFIX_KEY_INPUT)
		sb.WriteString("!")
	} else {
		sb.WriteString(": ID!")
	}
	for _, vd := range operation.VariableDefinitions {
		if !used[vd.Variable] {
			continue
		}
		sb.WriteString(", $")
		sb.WriteString(vd.Variable)
		sb.WriteString(": ")
		sb.WriteString(vd.Type.String())
		if vd.DefaultValue != nil {
			sb.WriteString(" = ")
			sb.WriteString(vd.DefaultValue.String())
		}
		if v, ok := variables[vd.Variable]; ok {
			args[vd.Variable] = v
		}
	}
	sb.WriteString(") { ")
	sb.WriteString(fieldName)
	if composite {
		sb.WriteString("(key: $")
	} else {
		sb.WriteString("(id: $")
	}
	sb.WriteString(pk)
	sb.WriteString(", limit: 1) { ")
	sb.WriteString(protocol.ITEMS)
	sb.WriteString(" ")
	writeSelectionSet(&sb, f.SelectionSet)
	sb.WriteString(" } }")

	reply := my.queryData(ctx, sb.String(), args, "")
	if reply.Data == nil && len(reply.Errors) > 0 { // 部分错误(如远程警告)与data共存时视为成功
		return nil, nil, false, fmt.Errorf("resolver回查失败: %w", reply.Errors)
	}
	wrapper, _ := reply.Data[fieldName].(map[string]interface{})
	items, _ := wrapper[protocol.ITEMS].([]interface{})
	if len(items) == 0 {
		return nil, reply.Errors, false, nil
	}
	return items[0], reply.Errors, true, nil
}

// writeSelectionSet 把客户端选择集序列化回GraphQL文本用于合成回查
// （fragment已在parse阶段inline展开，只需处理字段/别名/参数/嵌套）；
// 参数值直接用ast.Value.String()：变量引用保留$name形态，由合成查询声明后透传
func writeSelectionSet(sb *strings.Builder, set ast.SelectionSet) {
	sb.WriteString("{")
	for _, s := range set {
		f, ok := s.(*ast.Field)
		if !ok {
			continue
		}
		sb.WriteString(" ")
		if f.Alias != "" && f.Alias != f.Name {
			sb.WriteString(f.Alias)
			sb.WriteString(": ")
		}
		sb.WriteString(f.Name)
		if len(f.Arguments) > 0 {
			sb.WriteString("(")
			for i, a := range f.Arguments {
				if i > 0 {
					sb.WriteString(", ")
				}
				sb.WriteString(a.Name)
				sb.WriteString(": ")
				sb.WriteString(a.Value.String())
			}
			sb.WriteString(")")
		}
		if len(f.SelectionSet) > 0 {
			sb.WriteString(" ")
			writeSelectionSet(sb, f.SelectionSet)
		}
	}
	sb.WriteString(" }")
}

// collectVariables 收集选择集参数中引用到的变量名（含对象/列表嵌套）
func collectVariables(set ast.SelectionSet, used map[string]bool) {
	var walk func(v *ast.Value)
	walk = func(v *ast.Value) {
		if v == nil {
			return
		}
		if v.Kind == ast.Variable {
			used[v.Raw] = true
		}
		for _, child := range v.Children {
			walk(child.Value)
		}
	}
	for _, s := range set {
		switch node := s.(type) {
		case *ast.Field:
			for _, argument := range node.Arguments {
				walk(argument.Value)
			}
			collectVariables(node.SelectionSet, used)
		case *ast.InlineFragment:
			collectVariables(node.SelectionSet, used)
		}
	}
}
