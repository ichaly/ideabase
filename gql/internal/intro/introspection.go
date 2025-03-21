package intro

import (
	"context"
	"errors"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
)

// Handler GraphQL自省查询处理器
type Handler struct {
	schema *ast.Schema
}

// New 创建一个新的自省查询处理器
func New(schema *ast.Schema) *Handler {
	return &Handler{schema: schema}
}

// Introspect 处理自省查询
func (my *Handler) Introspect(ctx context.Context, query string, variables map[string]interface{}) (interface{}, error) {
	// 检查是否是自省查询
	if strings.Contains(query, "__schema") {
		return my.handleSchemaQuery(variables)
	} else if strings.Contains(query, "__type") {
		typeName, ok := variables["name"].(string)
		if !ok {
			return nil, errors.New("__type查询需要提供name参数")
		}
		return my.handleTypeQuery(typeName)
	}

	return nil, errors.New("不是有效的自省查询")
}

// handleSchemaQuery 处理__schema查询
func (my *Handler) handleSchemaQuery(variables map[string]interface{}) (interface{}, error) {
	result := map[string]interface{}{
		"__schema": my.getSchemaInfo(),
	}

	return result, nil
}

// handleTypeQuery 处理__type查询
func (my *Handler) handleTypeQuery(typeName string) (interface{}, error) {
	typeDef := my.schema.Types[typeName]
	if typeDef == nil {
		return map[string]interface{}{
			"__type": nil,
		}, nil
	}

	return map[string]interface{}{
		"__type": my.getFullType(typeDef),
	}, nil
}

// getSchemaInfo 获取Schema信息
func (my *Handler) getSchemaInfo() map[string]interface{} {
	result := map[string]interface{}{}

	// 添加查询、变更和订阅类型
	if my.schema.Query != nil {
		result["queryType"] = map[string]string{"name": my.schema.Query.Name}
	}

	if my.schema.Mutation != nil {
		result["mutationType"] = map[string]string{"name": my.schema.Mutation.Name}
	}

	if my.schema.Subscription != nil {
		result["subscriptionType"] = map[string]string{"name": my.schema.Subscription.Name}
	}

	// 添加所有类型
	types := make([]map[string]interface{}, 0, len(my.schema.Types))
	for _, typeDef := range my.schema.Types {
		// 跳过内部类型
		if !strings.HasPrefix(typeDef.Name, "__") {
			types = append(types, my.getFullType(typeDef))
		}
	}
	result["types"] = types

	// 添加所有指令
	directives := make([]map[string]interface{}, 0, len(my.schema.Directives))
	for _, directive := range my.schema.Directives {
		directives = append(directives, my.getDirective(directive))
	}
	result["directives"] = directives

	return result
}

// getFullType 获取完整类型信息
func (my *Handler) getFullType(def *ast.Definition) map[string]interface{} {
	result := map[string]interface{}{
		"kind": def.Kind,
		"name": def.Name,
	}

	if def.Description != "" {
		result["description"] = def.Description
	}

	// 处理字段
	if len(def.Fields) > 0 && (def.Kind == ast.Object || def.Kind == ast.Interface) {
		fields := make([]map[string]interface{}, 0, len(def.Fields))
		for _, field := range def.Fields {
			// 跳过内部字段
			if !strings.HasPrefix(field.Name, "__") {
				fields = append(fields, my.getField(field))
			}
		}
		result["fields"] = fields
	}

	// 处理输入字段
	if def.Kind == ast.InputObject && len(def.Fields) > 0 {
		inputFields := make([]map[string]interface{}, 0, len(def.Fields))
		for _, field := range def.Fields {
			inputFields = append(inputFields, my.getInputValue(field))
		}
		result["inputFields"] = inputFields
	}

	// 处理接口
	if (def.Kind == ast.Object || def.Kind == ast.Interface) && len(def.Interfaces) > 0 {
		interfaces := make([]map[string]interface{}, 0, len(def.Interfaces))
		for _, iface := range def.Interfaces {
			ifaceDef := my.schema.Types[iface]
			if ifaceDef != nil {
				interfaces = append(interfaces, my.getTypeRef(&ast.Type{NamedType: iface}))
			}
		}
		result["interfaces"] = interfaces
	}

	// 处理可能的类型
	if def.Kind == ast.Interface || def.Kind == ast.Union {
		possibleTypes := make([]map[string]interface{}, 0)
		for _, impl := range my.schema.GetPossibleTypes(def) {
			possibleTypes = append(possibleTypes, my.getTypeRef(&ast.Type{NamedType: impl.Name}))
		}
		result["possibleTypes"] = possibleTypes
	}

	// 处理枚举值
	if def.Kind == ast.Enum && len(def.EnumValues) > 0 {
		enumValues := make([]map[string]interface{}, 0, len(def.EnumValues))
		for _, value := range def.EnumValues {
			enumValues = append(enumValues, my.getEnumValue(value))
		}
		result["enumValues"] = enumValues
	}

	return result
}

// getField 获取字段信息
func (my *Handler) getField(field *ast.FieldDefinition) map[string]interface{} {
	result := map[string]interface{}{
		"name": field.Name,
		"type": my.getTypeRef(field.Type),
	}

	if field.Description != "" {
		result["description"] = field.Description
	}

	// 处理参数
	args := make([]map[string]interface{}, 0, len(field.Arguments))
	for _, arg := range field.Arguments {
		args = append(args, my.getInputValue(arg))
	}
	result["args"] = args

	// 处理弃用信息
	isDeprecated := false
	var deprecationReason string

	directive := field.Directives.ForName("deprecated")
	if directive != nil {
		isDeprecated = true
		reason := directive.Arguments.ForName("reason")
		if reason != nil && reason.Value != nil && reason.Value.Raw != "" {
			deprecationReason = reason.Value.Raw
		} else {
			deprecationReason = "No longer supported"
		}
	}

	result["isDeprecated"] = isDeprecated
	if isDeprecated {
		result["deprecationReason"] = deprecationReason
	}

	return result
}

// getInputValue 获取输入值信息
func (my *Handler) getInputValue(def interface{}) map[string]interface{} {
	result := map[string]interface{}{}

	switch d := def.(type) {
	case *ast.FieldDefinition:
		result["name"] = d.Name
		result["type"] = my.getTypeRef(d.Type)

		if d.Description != "" {
			result["description"] = d.Description
		}

		if d.DefaultValue != nil {
			result["defaultValue"] = d.DefaultValue.String()
		}

	case *ast.ArgumentDefinition:
		result["name"] = d.Name
		result["type"] = my.getTypeRef(d.Type)

		if d.Description != "" {
			result["description"] = d.Description
		}

		if d.DefaultValue != nil {
			result["defaultValue"] = d.DefaultValue.String()
		}
	}

	return result
}

// getEnumValue 获取枚举值信息
func (my *Handler) getEnumValue(value *ast.EnumValueDefinition) map[string]interface{} {
	result := map[string]interface{}{
		"name": value.Name,
	}

	if value.Description != "" {
		result["description"] = value.Description
	}

	// 处理弃用信息
	isDeprecated := false
	var deprecationReason string

	directive := value.Directives.ForName("deprecated")
	if directive != nil {
		isDeprecated = true
		reason := directive.Arguments.ForName("reason")
		if reason != nil && reason.Value != nil && reason.Value.Raw != "" {
			deprecationReason = reason.Value.Raw
		} else {
			deprecationReason = "No longer supported"
		}
	}

	result["isDeprecated"] = isDeprecated
	if isDeprecated {
		result["deprecationReason"] = deprecationReason
	}

	return result
}

// getTypeRef 获取类型引用
func (my *Handler) getTypeRef(t *ast.Type) map[string]interface{} {
	result := map[string]interface{}{}

	if t.NonNull {
		result["kind"] = "NON_NULL"
		result["ofType"] = my.getTypeRef(&ast.Type{
			NamedType: t.NamedType,
			Elem:      t.Elem,
		})
	} else if t.Elem != nil {
		result["kind"] = "LIST"
		result["ofType"] = my.getTypeRef(t.Elem)
	} else {
		def := my.schema.Types[t.NamedType]
		if def != nil {
			result["kind"] = def.Kind
			result["name"] = def.Name
		} else {
			result["kind"] = "SCALAR"
			result["name"] = t.NamedType
		}
	}

	return result
}

// getDirective 获取指令信息
func (my *Handler) getDirective(directive *ast.DirectiveDefinition) map[string]interface{} {
	result := map[string]interface{}{
		"name": directive.Name,
	}

	if directive.Description != "" {
		result["description"] = directive.Description
	}

	// 处理位置
	locations := make([]string, 0, len(directive.Locations))
	for _, loc := range directive.Locations {
		locations = append(locations, string(loc))
	}
	result["locations"] = locations

	// 处理参数
	args := make([]map[string]interface{}, 0, len(directive.Arguments))
	for _, arg := range directive.Arguments {
		args = append(args, my.getInputValue(arg))
	}
	result["args"] = args

	return result
}
