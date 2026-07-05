package gql

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/iancoleman/strcase"
	"github.com/jinzhu/inflection"
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
// Definition返回完整SDL片段：extend type Mutation/Query 的字段声明与所需的辅助
// input/type定义，随schema一起加载，自省与校验对Action与表CRUD一视同仁。
//
// Execute的返回值约定（回查补全）：
//   - 声明的返回类型是元数据实体且返回标量 → 视作实体id，引擎以客户端选择集回查
//     该实体（字段级resolver与关系查询在回查中原样生效）
//   - 返回map/切片/nil → 直接作为字段结果输出
type Action interface {
	Name() string       // 顶层字段名，如 botSave
	Definition() string // SDL片段：extend type Mutation { ... } 及辅助类型
	Execute(ctx context.Context, args map[string]interface{}) (interface{}, error)
}

// actionQuery 解析后发现顶层字段是Action：经error通道带出已解析的operation，
// 调用方（Execute）路由到Action执行，其余入口（订阅/持久化预热）按错误处理
type actionQuery struct {
	operation *ast.OperationDefinition
}

func (my *actionQuery) Error() string { return "Action操作不支持此入口" }

// RegisterAction 注册操作级Action并重建schema（SDL合并进模式，自省即时可见）。
// 仅限启动期调用：重建schema/自省/计划缓存的过程不与并发请求互斥。
func (my *Executor) RegisterAction(actions ...Action) error {
	for _, a := range actions {
		my.actions[a.Name()] = a
	}

	// 按名排序保证SDL拼接顺序稳定（map遍历随机会导致schema内容抖动）
	names := make([]string, 0, len(my.actions))
	for name := range my.actions {
		names = append(names, name)
	}
	sort.Strings(names)

	var sb strings.Builder
	sb.WriteString(my.source)
	for _, name := range names {
		sb.WriteString("\n")
		sb.WriteString(my.actions[name].Definition())
	}
	s, err := gqlparser.LoadSchema(&ast.Source{Name: "schema.graphql", Input: sb.String()})
	if err != nil {
		return fmt.Errorf("合并Action定义失败: %w", err)
	}
	my.schema = s
	my.intro = intro.New(s)
	my.cache = newPlanCache(planCacheSize)
	return nil
}

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

	fieldName := strcase.ToLowerCamel(inflection.Plural(className))
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

	reply := my.Execute(ctx, sb.String(), args, "")
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
