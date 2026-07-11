package gql

import (
	"context"
	"fmt"
	"reflect"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/ichaly/ideabase/gql/internal/intro"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"golang.org/x/sync/errgroup"
)

// Resolver 自定义字段解析器：处理无法用SQL表达的字段逻辑
// 元数据中通过 Field.Resolver 按名绑定，编译期跳过SQL，执行期填充结果
// 列表场景下Resolve会被并发调用，实现须线程安全；有状态逻辑请实现BatchResolver
type Resolver interface {
	Name() string
	Define() Define
	// Resolve 计算单个宿主对象的字段值，source为该对象已查出的字段
	Resolve(ctx context.Context, source any, args map[string]interface{}) (interface{}, error)
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
	Path  []string   // data根到宿主对象的字段别名路径（数组层级在执行期透明展开）
	Field string     // 要填充的字段别名
	Name  string     // resolver名或远程数据源名
	Key   string     // 远程绑定的宿主键别名（编译期补投影的内部列）
	node  *ast.Field // resolver字段AST引用：执行期按请求变量解出实参（随计划缓存复用）
}

// collectBindings 遍历操作选择集，收集所有resolver字段的绑定
func collectBindings(meta *Metadata, operation *ast.OperationDefinition) []binding {
	var bindings []binding

	var walk func(class *protocol.Class, set ast.SelectionSet, path []string)
	walk = func(class *protocol.Class, set ast.SelectionSet, path []string) {
		for _, s := range set {
			f, ok := s.(*ast.Field)
			if !ok {
				continue
			}
			if f.Name == protocol.ITEMS {
				walk(class, f.SelectionSet, append(path[:len(path):len(path)], f.Alias))
				continue
			}
			field, ok := class.Fields[f.Name]
			if !ok {
				continue
			}
			if field.Resolver != "" {
				bindings = append(bindings, binding{
					Path:  path,
					Field: f.Alias,
					Name:  field.Resolver,
					node:  f,
				})
				continue
			}
			if field.Remote != nil {
				bindings = append(bindings, binding{
					Path:  path,
					Field: f.Alias,
					Name:  field.Remote.Source,
					Key:   remoteKeyAlias(field.Remote.Key),
				})
				continue
			}
			if field.Column == "" && field.Relation != nil {
				if target, ok := meta.GetNode(field.Relation.TargetClass); ok {
					walk(target, f.SelectionSet, append(path[:len(path):len(path)], f.Alias))
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
		if ok {
			walk(class, f.SelectionSet, []string{f.Alias})
		}
	}
	return bindings
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

// resolve 按绑定填充后处理字段。远程关系绑定先行：网络取数并发（各job独立）、
// 回填串行（宿主map非并发安全），失败字段置null并以警告随响应errors返回（不中断）；
// resolver绑定随后：批量解析器整列表一次调用，普通解析器逐宿主并行计算
func (my *Executor) resolve(ctx context.Context, bindings []binding, data map[string]interface{}, variables map[string]interface{}) (gqlerror.List, error) {
	var warnings gqlerror.List
	var jobs []*remoteJob
	for _, b := range bindings {
		if b.Key == "" {
			continue
		}
		if sources := hosts(data, b.Path); len(sources) > 0 {
			jobs = append(jobs, &remoteJob{binding: b, sources: sources})
		}
	}
	if len(jobs) > 0 {
		group, gctx := errgroup.WithContext(ctx)
		for _, job := range jobs {
			group.Go(func() error { job.fetch(gctx, my.remotes); return nil })
		}
		_ = group.Wait() // job错误不经group传播(容错语义),此处仅同步
		for _, job := range jobs {
			job.fill()
			if job.err != nil {
				warnings = append(warnings, gqlerror.Wrap(job.err))
			}
		}
		for _, job := range jobs { // 全部回填后再剥内部键：多个远程可共用同一宿主键列
			job.strip()
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
		if many, ok := resolver.(BatchResolver); ok {
			err = safely(func() (err error) {
				values, err = many.ResolveBatch(ctx, sources, args)
				return
			})
			if err == nil && len(values) != len(sources) {
				err = fmt.Errorf("返回数量不匹配: 期望%d实际%d", len(sources), len(values))
			}
		} else {
			values, err = resolveEach(ctx, resolver, sources, args)
		}
		if err != nil {
			return warnings, fmt.Errorf("resolver %s 执行失败: %w", b.Name, err)
		}
		for i, source := range sources {
			source[b.Field] = values[i]
		}
	}
	return warnings, nil
}

// resolveEach 普通resolver逐宿主有界并发计算，返回与sources对位的结果
func resolveEach(ctx context.Context, resolver Resolver, sources []map[string]interface{}, args map[string]interface{}) ([]interface{}, error) {
	values := make([]interface{}, len(sources))
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(8)
	for i, source := range sources {
		group.Go(func() error {
			return safely(func() (err error) {
				values[i], err = resolver.Resolve(ctx, source, args)
				return
			})
		})
	}
	return values, group.Wait()
}

// safely 拦截用户实现（resolver/remote）的panic转为错误：
// errgroup不recover，goroutine里的panic会打崩整个服务进程，引擎边界必须兜底
func safely(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	return fn()
}

// Define 是Resolver/Remote注册即声明的schema形状。
type Define struct {
	Class    string // Query、Mutation或宿主实体名
	Name     string // 字段名，如 botSave、greeting
	Doc      string // 字段描述，渲染为SDL文档字符串
	Args     string // 参数签名原文，如 "nickname: String!, avatar: String"；空=无参
	Result   string // 返回类型，如 BotProfile、Int
	Extra    string // 附加SDL（辅助input/type等声明），重建时按文本去重并入schema
	Existing bool   // 绑定schema已有字段；仅允许通过显式Replace注册
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
func (my *Executor) mount(m mounted) error {
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
	field.Name, field.Type, field.Description, field.Virtual = d.Name, d.Result, d.Doc, true
	class.Fields[d.Name] = field
	return nil
}

// rebuild 合并全部注册声明重建schema（Resolver/Remote共用；仅限启动期）：
// extend片段按注册键排序保证文本稳定，附加SDL按内容去重（多个声明共享同一虚拟类型）
func (my *Executor) rebuild() error {
	defines := make(map[string]Define, len(my.resolvers)+len(my.remotes))
	for name, r := range my.resolvers {
		defines[name] = r.Define()
	}
	for name, r := range my.remotes {
		if m, ok := r.(mounted); ok {
			defines[name] = m.Define()
		}
	}

	names := make([]string, 0, len(defines))
	for name := range defines {
		names = append(names, name)
	}
	sort.Strings(names)

	var sb strings.Builder
	sb.WriteString(my.source)
	extras := make(map[string]bool)
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
	}
	s, err := gqlparser.LoadSchema(&ast.Source{Name: "schema.graphql", Input: sb.String()})
	if err != nil {
		return fmt.Errorf("合并注册声明失败: %w", err)
	}
	my.schema = s
	my.intro = intro.New(s)
	my.cache = newPlanCache(planCacheSize)
	return nil
}

// rootResolverQuery 携带包含自定义根字段Resolver的已解析operation。
type rootResolverQuery struct{ operation *ast.OperationDefinition }

func (my *rootResolverQuery) Error() string { return "根字段Resolver不支持此入口" }

func (my *Executor) hasRootResolver(set ast.SelectionSet) bool {
	for _, s := range set {
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
	}
	return false
}

func (my *Executor) executeRootResolvers(ctx context.Context, operation *ast.OperationDefinition, variables map[string]interface{}) gqlReply {
	typename := "Query"
	if operation.Operation == ast.Mutation {
		typename = "Mutation"
	}
	data := make(map[string]interface{}, len(operation.SelectionSet))
	var warnings gqlerror.List
	for _, s := range operation.SelectionSet {
		f, ok := s.(*ast.Field)
		if !ok {
			continue
		}
		if f.Name == "__typename" {
			data[f.Alias] = typename
			continue
		}
		value, warns, err := my.resolveRootField(ctx, operation, f, variables, typename)
		if err != nil {
			return gqlReply{Errors: gqlerror.List{gqlerror.Wrap(err)}}
		}
		warnings = append(warnings, warns...)
		data[f.Alias] = value
	}
	return gqlReply{Data: data, Errors: warnings}
}

func (my *Executor) resolveRootField(ctx context.Context, operation *ast.OperationDefinition, field *ast.Field, variables map[string]interface{}, parent string) (interface{}, gqlerror.List, error) {
	resolver := my.resolvers[parent+"."+field.Name]
	if resolver == nil {
		return my.executeDefaultRootField(ctx, operation, field, variables)
	}
	var result interface{}
	err := safely(func() (err error) {
		result, err = resolver.Resolve(ctx, Root{}, field.ArgumentMap(variables))
		return
	})
	if err != nil {
		return nil, nil, err
	}
	return my.enrich(ctx, operation, field, variables, result)
}

// executeDefaultRootField 在自定义根Resolver与默认数据库字段混排时逐字段复用原
// SQL编译/执行链；默认-only操作仍走整份operation单SQL快路径，零额外开销。
func (my *Executor) executeDefaultRootField(ctx context.Context, operation *ast.OperationDefinition, field *ast.Field, variables map[string]interface{}) (interface{}, gqlerror.List, error) {
	if my.compiler == nil || my.database == nil {
		return nil, nil, fmt.Errorf("执行器未配置数据库或编译器")
	}
	sub := *operation
	sub.SelectionSet = ast.SelectionSet{field}
	plan, err := my.compiler.Compile(&sub, variables)
	if err != nil {
		return nil, nil, err
	}
	plan.resolvers = collectBindings(my.metadata, &sub)
	plan.paths = collectCodecPaths(sub.SelectionSet, my.metadata)
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
func (my *Executor) enrich(ctx context.Context, operation *ast.OperationDefinition, f *ast.Field, variables map[string]interface{}, result interface{}) (interface{}, gqlerror.List, error) {
	if result == nil || len(f.SelectionSet) == 0 {
		return result, nil, nil
	}
	switch result.(type) {
	case map[string]interface{}, []interface{}:
		return result, nil, nil
	}
	className := f.Definition.Type.Name()
	if _, ok := my.metadata.GetNode(className); !ok {
		return result, nil, nil
	}
	// 类型化Resolver返回实体结构体：取Id触发回查，与返回标量id等价
	if v := reflect.Indirect(reflect.ValueOf(result)); v.Kind() == reflect.Struct {
		if id := v.FieldByName("Id"); id.IsValid() {
			result = id.Interface()
		}
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
	sb.WriteString(": ID!")
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
	sb.WriteString("(id: $")
	sb.WriteString(pk)
	sb.WriteString(", limit: 1) { ")
	sb.WriteString(protocol.ITEMS)
	sb.WriteString(" ")
	writeSelectionSet(&sb, f.SelectionSet)
	sb.WriteString(" } }")

	reply := my.queryData(ctx, sb.String(), args, "")
	if reply.Data == nil && len(reply.Errors) > 0 { // 部分错误(如远程警告)与data共存时视为成功
		return nil, nil, fmt.Errorf("Resolver回查失败: %w", reply.Errors)
	}
	wrapper, _ := reply.Data[fieldName].(map[string]interface{})
	items, _ := wrapper[protocol.ITEMS].([]interface{})
	if len(items) == 0 {
		return nil, reply.Errors, nil
	}
	return items[0], reply.Errors, nil
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
		f, ok := s.(*ast.Field)
		if !ok {
			continue
		}
		for _, a := range f.Arguments {
			walk(a.Value)
		}
		collectVariables(f.SelectionSet, used)
	}
}
