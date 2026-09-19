package gql

import (
	stdjson "encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

type completionMode uint8

const (
	completionLocal completionMode = iota
	completionRemote
)

type completionOptions struct {
	mode       completionMode
	parentType string
	fieldName  string
	typename   func(string) string
}

// completeValue is the single response completion path for local resolvers and
// remote endpoint responses. The mode only changes the public error contract.
func completeValue(schema *ast.Schema, value any, typ *ast.Type, set ast.SelectionSet, path ast.Path, known gqlerror.List, options completionOptions) (any, gqlerror.List, bool) {
	if value == nil {
		if typ != nil && typ.NonNull {
			if endpointErrorCovers(known, path) {
				return nil, nil, true
			}
			if options.mode == completionRemote {
				err := gqlerror.ErrorPathf(path, "endpoint returned null for non-null field")
				err.Extensions = map[string]any{"code": "BAD_GATEWAY_RESPONSE"}
				return nil, gqlerror.List{err}, true
			}
			name := options.fieldName
			if name == "" {
				name = lastPathName(path)
			}
			message := "Cannot return null for non-nullable field"
			if options.parentType != "" && name != "" {
				message += " " + options.parentType + "." + name
			}
			return nil, gqlerror.List{gqlerror.ErrorPathf(path, "%s", message)}, true
		}
		return nil, nil, false
	}
	if typ != nil && typ.Elem != nil {
		items, ok := value.([]any)
		if !ok {
			return invalidCompletedValue(path, typ, known, options)
		}
		out := make([]any, len(items))
		var errs gqlerror.List
		for index, item := range items {
			child, childErrs, violation := completeValue(schema, item, typ.Elem, set, appendCompletionPath(path, ast.PathIndex(index)), known, options)
			errs = append(errs, childErrs...)
			if violation {
				return nil, errs, typ.NonNull
			}
			out[index] = child
		}
		return out, errs, false
	}
	if len(set) == 0 {
		if completed, ok := coerceCompletedScalar(schema, typ, value); ok {
			return completed, nil, false
		}
		return invalidCompletedValue(path, typ, known, options)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return invalidCompletedValue(path, typ, known, options)
	}
	out := make(map[string]any, len(set))
	var errs gqlerror.List
	runtimeType := ""
	if hasInlineFragment(set) {
		alias := gatewayTypeAlias(set)
		if options.mode == completionLocal {
			alias = "__typename"
		}
		var valid bool
		runtimeType, valid = object[alias].(string)
		if options.typename != nil {
			runtimeType = options.typename(runtimeType)
		}
		object[alias] = runtimeType
		definition := schema.Types[runtimeType]
		if !valid || definition == nil || definition.Kind != ast.Object {
			return invalidCompletedValue(path, typ, known, options)
		}
	}
	for _, selection := range set {
		switch node := selection.(type) {
		case *ast.Field:
			key := responseKey(node)
			fieldPath := appendCompletionPath(path, ast.PathName(key))
			fieldType := (*ast.Type)(nil)
			if node.Definition != nil {
				fieldType = node.Definition.Type
			} else if node.Name == "__typename" {
				fieldType = ast.NonNullNamedType("String", nil)
			}
			childOptions := options
			if typ != nil {
				childOptions.parentType = typ.NamedType
			}
			childOptions.fieldName = node.Name
			if node.Name == "__typename" {
				name, valid := object[key].(string)
				if options.typename != nil {
					name = options.typename(name)
				}
				object[key] = name
				definition := schema.Types[name]
				if !valid || definition == nil || definition.Kind != ast.Object {
					return invalidCompletedValue(fieldPath, fieldType, known, options)
				}
			}
			child, childErrs, violation := completeValue(schema, object[key], fieldType, node.SelectionSet, fieldPath, known, childOptions)
			errs = append(errs, childErrs...)
			if violation {
				return nil, errs, typ != nil && typ.NonNull
			}
			out[key] = child
		case *ast.InlineFragment:
			if !typeConditionApplies(schema, node.TypeCondition, runtimeType) {
				continue
			}
			child, childErrs, violation := completeValue(schema, object, typ, node.SelectionSet, path, known, options)
			errs = append(errs, childErrs...)
			if violation {
				return nil, errs, typ != nil && typ.NonNull
			}
			fragment, valid := child.(map[string]any)
			if !valid {
				return nil, errs, typ != nil && typ.NonNull
			}
			for key, value := range fragment {
				out[key] = value
			}
		}
	}
	return out, errs, false
}

func invalidCompletedValue(path ast.Path, typ *ast.Type, known gqlerror.List, options completionOptions) (any, gqlerror.List, bool) {
	if endpointErrorCovers(known, path) {
		return nil, nil, typ != nil && typ.NonNull
	}
	message := "resolver returned a value with the wrong shape"
	if options.mode == completionRemote {
		message = "endpoint returned a value with the wrong shape"
	}
	err := gqlerror.ErrorPathf(path, "%s", message)
	if options.mode == completionRemote {
		err.Extensions = map[string]any{"code": "BAD_GATEWAY_RESPONSE"}
	} else {
		err.Extensions = map[string]any{"code": "RESOLVER_ERROR"}
	}
	return nil, gqlerror.List{err}, typ != nil && typ.NonNull
}

func lastPathName(path ast.Path) string {
	for i := len(path) - 1; i >= 0; i-- {
		if name, ok := path[i].(ast.PathName); ok {
			return string(name)
		}
	}
	return ""
}

func appendCompletionPath(path ast.Path, element ast.PathElement) ast.Path {
	out := make(ast.Path, len(path), len(path)+1)
	copy(out, path)
	return append(out, element)
}

func coerceCompletedScalar(schema *ast.Schema, typ *ast.Type, value any) (any, bool) {
	if typ == nil {
		return value, true
	}
	switch typ.NamedType {
	case "BigInt", "Decimal":
		text, err := exactNumber(typ.NamedType, value)
		return text, err == nil
	case "String":
		_, ok := value.(string)
		return value, ok
	case "ID":
		if _, ok := value.(string); ok {
			return value, true
		}
		if integer, ok := completedInteger(value); ok {
			return strconv.FormatInt(integer, 10), true
		}
		return nil, false
	case "Int":
		integer, ok := completedInteger(value)
		return integer, ok && integer >= math.MinInt32 && integer <= math.MaxInt32
	case "Float":
		number, ok := completedFloat(value)
		return value, ok && !math.IsInf(number, 0) && !math.IsNaN(number)
	case "Boolean":
		_, ok := value.(bool)
		return value, ok
	}
	definition := schema.Types[typ.NamedType]
	if definition == nil || definition.Kind != ast.Enum {
		return value, true
	}
	name, ok := value.(string)
	return value, ok && definition.EnumValues.ForName(name) != nil
}

func completedInteger(value any) (int64, bool) {
	switch value := value.(type) {
	case stdjson.Number:
		integer, err := value.Int64()
		return integer, err == nil
	case int:
		return int64(value), true
	case int8:
		return int64(value), true
	case int16:
		return int64(value), true
	case int32:
		return int64(value), true
	case int64:
		return value, true
	case uint:
		return unsignedInteger(uint64(value))
	case uint8:
		return int64(value), true
	case uint16:
		return int64(value), true
	case uint32:
		return int64(value), true
	case uint64:
		return unsignedInteger(value)
	case float64:
		if value >= math.MinInt64 && value <= math.MaxInt64 && value == math.Trunc(value) {
			return int64(value), true
		}
		return 0, false
	default:
		return 0, false
	}
}

func unsignedInteger(value uint64) (int64, bool) {
	return int64(value), value <= math.MaxInt64
}

func completedFloat(value any) (float64, bool) {
	switch value := value.(type) {
	case stdjson.Number:
		number, err := value.Float64()
		return number, err == nil
	case float64:
		return value, true
	case float32:
		return float64(value), true
	default:
		integer, ok := completedInteger(value)
		return float64(integer), ok
	}
}

func endpointErrorCovers(errors gqlerror.List, path ast.Path) bool {
	want := path.String()
	for _, err := range errors {
		have := err.Path.String()
		if have == want || (want != "" && strings.HasPrefix(have, want+".")) || (want != "" && strings.HasPrefix(have, want+"[")) {
			return true
		}
	}
	return false
}
