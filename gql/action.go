package gql

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/ichaly/ideabase/gql/internal/intro"
	"github.com/ichaly/ideabase/gql/protocol"
)

// Action 操作级扩展：自定义顶层Query/Mutation字段，绕过SQL编译由Go承载业务编排
// （多步事务、跨服务调用等无法用单条SQL表达的动作）。与字段级Resolver互补：
// Resolver填充实体上的一个字段，Action接管整个顶层操作。
//
// Execute的返回值约定（回查补全）：
//   - 声明的返回类型是元数据实体且返回标量 → 视作实体id，引擎以客户端选择集回查
//     该实体（字段级resolver与关系查询在回查中原样生效）
//   - 返回map/切片/nil → 直接作为字段结果输出
type Action interface {
	Define() Define // schema声明：SDL由引擎渲染合并，自省与校验同表CRUD一视同仁
	Execute(ctx context.Context, args map[string]interface{}) (interface{}, error)
}

// Define 注册即声明的schema形状：Action与字段级Resolver/Remote共用，
// Class决定挂载点（空=Mutation/Query根，非空=该实体的字段）
type Define struct {
	Class  string // 宿主实体名（Resolver/Remote专用），空表示挂根
	Name   string // 字段名，如 botSave、greeting
	Doc    string // 字段描述，渲染为SDL文档字符串
	Args   string // 参数签名原文，如 "nickname: String!, avatar: String"；空=无参
	Result string // 返回类型，如 BotProfile、Int
	Query  bool   // 挂载到Query根（缺省Mutation；Class非空时无效）
	Extra  string // 附加SDL（辅助input/type等声明），重建时按文本去重并入schema
}

// sdl 渲染声明为extend片段（文档用三引号块，内容含引号也合法；Extra由rebuild单独并入）
func (my Define) sdl() string {
	kind, args, doc := "Mutation", "", ""
	if my.Query {
		kind = "Query"
	}
	if my.Class != "" {
		kind = my.Class
	}
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

// rebuild 合并全部注册声明重建schema（Action/Resolver/Remote共用；仅限启动期）：
// extend片段按注册键排序保证文本稳定，附加SDL按内容去重（多个声明共享同一虚拟类型）
func (my *Executor) rebuild() error {
	defines := make(map[string]Define, len(my.actions)+len(my.resolvers)+len(my.remotes))
	for name, a := range my.actions {
		defines[name] = a.Define()
	}
	for name, r := range my.resolvers {
		if m, ok := r.(mounted); ok {
			defines[name] = m.Define()
		}
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
		sb.WriteString("\n")
		sb.WriteString(d.sdl())
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

// actionQuery 解析后发现顶层字段是Action：经error通道带出已解析的operation，
// 调用方（Execute）路由到Action执行，其余入口（订阅/持久化预热）按错误处理
type actionQuery struct {
	operation *ast.OperationDefinition
}

func (my *actionQuery) Error() string { return "Action操作不支持此入口" }

// checkActions 顶层选择集含Action字段时校验并返回true；
// Action与实体字段语义不同（无SQL计划），不允许同一操作混排。
func (my *Executor) checkActions(set ast.SelectionSet) (bool, error) {
	hit, miss := 0, 0
	for _, s := range set {
		if f, ok := s.(*ast.Field); ok {
			if strings.HasPrefix(f.Name, "__") {
				continue // __typename等内省元字段不计入混排统计（Apollo客户端默认注入）
			}
			if _, ok := my.actions[f.Name]; ok {
				hit++
			} else {
				miss++
			}
		}
	}
	if hit == 0 {
		return false, nil
	}
	if miss > 0 {
		return true, fmt.Errorf("Action操作不可与实体字段混排于同一请求")
	}
	return true, nil
}

// executeActions 顺序执行操作内的全部Action字段（变更语义按GraphQL规范串行）；
// 回查产生的非致命警告以GraphQL部分错误语义与data共存返回
func (my *Executor) executeActions(ctx context.Context, operation *ast.OperationDefinition, variables map[string]interface{}) gqlReply {
	typename := "Query"
	if operation.Operation == ast.Mutation {
		typename = "Mutation"
	}
	var warnings gqlerror.List
	data := make(map[string]interface{}, len(operation.SelectionSet))
	for _, s := range operation.SelectionSet {
		f, ok := s.(*ast.Field)
		if !ok {
			continue
		}
		if strings.HasPrefix(f.Name, "__") {
			if f.Name == "__typename" {
				data[f.Alias] = typename // 按操作类型回填根类型名
			}
			continue
		}
		action := my.actions[f.Name]
		result, err := action.Execute(ctx, f.ArgumentMap(variables))
		if err != nil {
			return gqlReply{Errors: gqlerror.List{gqlerror.Wrap(err)}}
		}
		value, warns, err := my.enrich(ctx, operation, f, variables, result)
		if err != nil {
			return gqlReply{Errors: gqlerror.List{gqlerror.Wrap(err)}}
		}
		warnings = append(warnings, warns...)
		data[f.Alias] = value
	}
	return gqlReply{Data: data, Errors: warnings}
}

// enrich 回查补全：Action返回实体id时，以客户端选择集合成实体查询走既有
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
	// 类型化Action（NewAction）返回实体结构体：取Id触发回查，与返回标量id等价
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

	reply := my.run(ctx, sb.String(), args, "")
	if reply.Data == nil && len(reply.Errors) > 0 { // 部分错误(如远程警告)与data共存时视为成功
		return nil, nil, fmt.Errorf("Action回查失败: %w", reply.Errors)
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
