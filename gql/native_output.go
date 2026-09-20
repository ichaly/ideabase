package gql

import (
	"slices"

	"github.com/vektah/gqlparser/v2/ast"
)

// 同一次原生SQL读取客户端投影与强类型返回值依赖；内部别名不泄露到响应。
type outputField struct {
	name, alias string
	children    []outputField
	internal    bool
}

func (my *inputBinding) project(schema *ast.Schema, typ *ast.Type, selection *ast.SelectionSet) []outputField {
	if my.elem != nil {
		return my.elem.project(schema, typ.Elem, selection)
	}
	if my.scalar != nil && len(*selection) > 0 {
		return selectedOutput(*selection)
	}
	var fields []outputField
	for _, field := range my.fields {
		kind := schema.Types[typ.Name()]
		definition := kind.Fields.ForName(field.name)
		node := &ast.Field{Name: field.name, Alias: nativeAlias(*selection), Definition: definition, ObjectDefinition: kind}
		index := -1
		for i, item := range *selection {
			if existing, ok := item.(*ast.Field); ok && existing.Name == field.name {
				copy := *existing
				copy.SelectionSet = slices.Clone(existing.SelectionSet)
				node = &copy
				index = i
				break
			}
		}
		children := field.plan.project(schema, definition.Type, &node.SelectionSet)
		if index < 0 {
			*selection = append(*selection, node)
		} else {
			(*selection)[index] = node
		}
		fields = append(fields, outputField{field.name, node.Alias, children, index < 0})
	}
	return fields
}

func selectedOutput(selection ast.SelectionSet) []outputField {
	fields := make([]outputField, 0, len(selection))
	for _, item := range selection {
		if field, ok := item.(*ast.Field); ok {
			fields = append(fields, outputField{field.Name, responseKey(field), selectedOutput(field.SelectionSet), false})
		}
	}
	return fields
}

func nativeAlias(selection ast.SelectionSet) string {
	alias := "_resolver"
	for slices.ContainsFunc(selection, func(item ast.Selection) bool { field, ok := item.(*ast.Field); return ok && field.Alias == alias }) {
		alias += "_"
	}
	return alias
}

func extractOutput(value any, fields []outputField) any {
	if len(fields) == 0 {
		return cloneVariableValue(value)
	}
	switch v := value.(type) {
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = extractOutput(item, fields)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(fields))
		for _, field := range fields {
			out[field.name] = extractOutput(v[field.alias], field.children)
			if field.internal {
				delete(v, field.alias)
			}
		}
		return out
	}
	return value
}

func mergeOutput(original, changed any, selection ast.SelectionSet) any {
	if changed == nil {
		return nil
	}
	switch value := changed.(type) {
	case []any:
		previous, _ := original.([]any)
		out := make([]any, len(value))
		for i, item := range value {
			var old any
			if i < len(previous) {
				old = previous[i]
			}
			out[i] = mergeOutput(old, item, selection)
		}
		return out
	case map[string]any:
		out, _ := original.(map[string]any)
		if out == nil {
			out = make(map[string]any)
		}
		for _, item := range selection {
			if field, ok := item.(*ast.Field); ok {
				if child, exists := value[field.Name]; exists {
					key := responseKey(field)
					out[key] = mergeOutput(out[key], child, field.SelectionSet)
				}
			}
		}
		return out
	}
	return changed
}
