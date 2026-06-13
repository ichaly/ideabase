// Package intro GraphQL自省查询处理
// 启动时从schema构建一份符合规范的完整自省数据集（含__Type/ofType链、
// inputFields、enumValues、directives等），请求时按客户端查询形状投影返回，
// 支持别名与fragment——GraphiQL/Apollo codegen的标准IntrospectionQuery可直接对接。
// 自省结果与schema严格一致：schema没有的能力不存在任何隐藏入口。
package intro

import (
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/utl"
	"github.com/vektah/gqlparser/v2/ast"
)

// Handler GraphQL自省查询处理器
type Handler struct {
	schema *ast.Schema
	data   map[string]interface{} // __schema完整数据集
	types  map[string]interface{} // 类型名 -> __Type完整数据（含相互引用）
}

// New 创建自省处理器并构建数据集
func New(schema *ast.Schema) *Handler {
	my := &Handler{schema: schema, types: make(map[string]interface{}, len(schema.Types))}
	my.build()
	return my
}

// Introspect 按选择集投影自省数据集
// operation须已完成解析校验与fragment展开（调用方统一处理，全请求只解析一次）
func (my *Handler) Introspect(operation *ast.OperationDefinition, variables map[string]interface{}) (map[string]interface{}, error) {
	result := make(map[string]interface{})
	for _, field := range fieldsOf(operation.SelectionSet) {
		switch field.Name {
		case "__typename":
			result[field.Alias] = "Query"
		case "__schema":
			result[field.Alias] = my.project(field.SelectionSet, my.data)
		case "__type":
			name, err := typeArgument(field, variables)
			if err != nil {
				return nil, err
			}
			if value, ok := my.types[name]; ok {
				result[field.Alias] = my.project(field.SelectionSet, value)
			} else {
				result[field.Alias] = nil
			}
		default:
			return nil, fmt.Errorf("自省查询不支持与数据字段混合: %s", field.Name)
		}
	}
	return result, nil
}

// typeArgument 解析__type的name参数（字面量或变量）
func typeArgument(field *ast.Field, variables map[string]interface{}) (string, error) {
	arg := field.Arguments.ForName("name")
	if arg == nil || arg.Value == nil {
		return "", fmt.Errorf("__type查询需要提供name参数")
	}
	if arg.Value.Kind == ast.Variable {
		if name, ok := variables[arg.Value.Raw].(string); ok {
			return name, nil
		}
		return "", fmt.Errorf("__type的name变量缺失或不是字符串")
	}
	return arg.Value.Raw, nil
}

// project 按选择集投影数据：map按字段取值，数组逐项递归，叶子原样返回
func (my *Handler) project(set ast.SelectionSet, value interface{}) interface{} {
	switch node := value.(type) {
	case []interface{}:
		out := make([]interface{}, len(node))
		for i, item := range node {
			out[i] = my.project(set, item)
		}
		return out
	case map[string]interface{}:
		if len(set) == 0 {
			return nil
		}
		out := make(map[string]interface{}, len(set))
		for _, field := range fieldsOf(set) {
			child, ok := node[field.Name]
			if !ok || child == nil {
				out[field.Alias] = nil
				continue
			}
			if len(field.SelectionSet) > 0 {
				out[field.Alias] = my.project(field.SelectionSet, child)
			} else {
				out[field.Alias] = child
			}
		}
		return out
	default:
		return node
	}
}

// fieldsOf 选择集中的纯字段列表（fragment已由调用方inline展开）
func fieldsOf(set ast.SelectionSet) []*ast.Field {
	fields := make([]*ast.Field, 0, len(set))
	for _, selection := range set {
		if field, ok := selection.(*ast.Field); ok {
			fields = append(fields, field)
		}
	}
	return fields
}

// ---------- 数据集构建 ----------

// build 构建__schema数据集；类型先建空容器再填充，支持相互引用与ofType环
func (my *Handler) build() {
	names := utl.SortKeys(my.schema.Types)
	for _, name := range names {
		my.types[name] = map[string]interface{}{"__typename": "__Type"}
	}

	typeList := make([]interface{}, 0, len(names))
	for _, name := range names {
		my.fillType(my.schema.Types[name])
		typeList = append(typeList, my.types[name])
	}

	directives := make([]interface{}, 0, len(my.schema.Directives))
	for _, name := range utl.SortKeys(my.schema.Directives) {
		directives = append(directives, my.directive(my.schema.Directives[name]))
	}

	my.data = map[string]interface{}{
		"__typename":       "__Schema",
		"description":      nil,
		"queryType":        my.rootRef(my.schema.Query),
		"mutationType":     my.rootRef(my.schema.Mutation),
		"subscriptionType": my.rootRef(my.schema.Subscription),
		"types":            typeList,
		"directives":       directives,
	}
}

// rootRef 根操作类型引用
func (my *Handler) rootRef(def *ast.Definition) interface{} {
	if def == nil {
		return nil
	}
	return my.types[def.Name]
}

// fillType 填充单个类型的完整__Type数据
func (my *Handler) fillType(def *ast.Definition) {
	node := my.types[def.Name].(map[string]interface{})
	node["kind"] = string(def.Kind)
	node["name"] = def.Name
	node["description"] = description(def.Description)
	node["fields"] = nil
	node["inputFields"] = nil
	node["interfaces"] = nil
	node["possibleTypes"] = nil
	node["enumValues"] = nil
	node["ofType"] = nil
	node["specifiedByURL"] = nil

	switch def.Kind {
	case ast.Object, ast.Interface:
		fields := make([]interface{}, 0, len(def.Fields))
		for _, field := range def.Fields {
			if strings.HasPrefix(field.Name, "__") {
				continue // 元字段不出现在fields列表（与规范一致）
			}
			fields = append(fields, map[string]interface{}{
				"__typename":        "__Field",
				"name":              field.Name,
				"description":       description(field.Description),
				"args":              my.inputValues(field.Arguments),
				"type":              my.typeRef(field.Type),
				"isDeprecated":      false,
				"deprecationReason": nil,
			})
		}
		node["fields"] = fields

		interfaces := make([]interface{}, 0, len(def.Interfaces))
		for _, name := range def.Interfaces {
			interfaces = append(interfaces, my.types[name])
		}
		node["interfaces"] = interfaces
	case ast.InputObject:
		inputs := make([]interface{}, 0, len(def.Fields))
		for _, field := range def.Fields {
			inputs = append(inputs, map[string]interface{}{
				"__typename":   "__InputValue",
				"name":         field.Name,
				"description":  description(field.Description),
				"type":         my.typeRef(field.Type),
				"defaultValue": defaultValue(field.DefaultValue),
			})
		}
		node["inputFields"] = inputs
	case ast.Enum:
		values := make([]interface{}, 0, len(def.EnumValues))
		for _, value := range def.EnumValues {
			values = append(values, map[string]interface{}{
				"__typename":        "__EnumValue",
				"name":              value.Name,
				"description":       description(value.Description),
				"isDeprecated":      false,
				"deprecationReason": nil,
			})
		}
		node["enumValues"] = values
	case ast.Union:
		possible := make([]interface{}, 0)
		for _, t := range my.schema.PossibleTypes[def.Name] {
			possible = append(possible, my.types[t.Name])
		}
		node["possibleTypes"] = possible
	}
}

// typeRef 类型引用：NON_NULL/LIST包装层为独立节点，命名类型直接引用完整__Type
func (my *Handler) typeRef(t *ast.Type) interface{} {
	if t.NonNull {
		return wrap("NON_NULL", my.typeRef(&ast.Type{NamedType: t.NamedType, Elem: t.Elem}))
	}
	if t.Elem != nil {
		return wrap("LIST", my.typeRef(t.Elem))
	}
	return my.types[t.NamedType]
}

// wrap 构造包装类型节点（除kind/ofType外其余字段为null，符合规范）
func wrap(kind string, inner interface{}) map[string]interface{} {
	return map[string]interface{}{
		"__typename": "__Type",
		"kind":       kind,
		"name":       nil,
		"ofType":     inner,
	}
}

// inputValues 参数列表转__InputValue
func (my *Handler) inputValues(args ast.ArgumentDefinitionList) []interface{} {
	values := make([]interface{}, 0, len(args))
	for _, arg := range args {
		values = append(values, map[string]interface{}{
			"__typename":   "__InputValue",
			"name":         arg.Name,
			"description":  description(arg.Description),
			"type":         my.typeRef(arg.Type),
			"defaultValue": defaultValue(arg.DefaultValue),
		})
	}
	return values
}

// directive 指令定义转__Directive
func (my *Handler) directive(def *ast.DirectiveDefinition) map[string]interface{} {
	locations := make([]interface{}, 0, len(def.Locations))
	for _, location := range def.Locations {
		locations = append(locations, string(location))
	}
	return map[string]interface{}{
		"__typename":   "__Directive",
		"name":         def.Name,
		"description":  description(def.Description),
		"locations":    locations,
		"args":         my.inputValues(def.Arguments),
		"isRepeatable": def.IsRepeatable,
	}
}

// description 空描述返回null
func description(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// defaultValue 默认值的GraphQL字面量表示
func defaultValue(value *ast.Value) interface{} {
	if value == nil {
		return nil
	}
	return value.String()
}
