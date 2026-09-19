package gql

import (
	stdjson "encoding/json"
	"math"
	"reflect"
	"strconv"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// coerceVariables implements the request-time input coercion boundary. Query
// validation has already linked every definition to the effective schema; doing
// the small runtime walk here avoids gqlparser's intentionally loose numeric and
// case-insensitive compatibility rules. The request map is updated in place so
// all planner/handler consumers observe the same defaults and canonical lists.
func coerceVariables(schema *ast.Schema, op *ast.OperationDefinition, variables map[string]any) (map[string]any, error) {
	if op == nil || len(op.VariableDefinitions) == 0 {
		return variables, nil
	}
	if variables == nil {
		variables = map[string]any{}
	}
	coerced := make(map[string]any, len(op.VariableDefinitions))
	for _, def := range op.VariableDefinitions {
		path := ast.Path{ast.PathName("variable"), ast.PathName(def.Variable)}
		value, present := variables[def.Variable]
		if !present && def.DefaultValue != nil {
			var err error
			value, err = def.DefaultValue.Value(nil)
			if err != nil {
				return nil, gqlerror.WrapPath(path, err)
			}
			present = true
		}
		if !present {
			if def.Type.NonNull {
				return nil, gqlerror.ErrorPathf(path, "must be defined")
			}
			continue
		}
		value, strictErr := coerceVariableValue(schema, def.Type, value, path)
		if strictErr != nil {
			return nil, strictErr
		}
		coerced[def.Variable] = value
	}
	assignVariables(variables, coerced)
	return variables, nil
}

func assignVariables(dst, src map[string]any) {
	if dst == nil {
		return
	}
	for key := range dst {
		delete(dst, key)
	}
	for key, value := range src {
		dst[key] = value
	}
}

func cloneVariables(variables map[string]any) map[string]any {
	out := make(map[string]any, len(variables))
	for key, value := range variables {
		out[key] = cloneVariableValue(value)
	}
	return out
}

func cloneVariableValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneVariables(value)
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = cloneVariableValue(item)
		}
		return out
	default:
		return value
	}
}

// coerceVariableValue closes gqlparser's deliberately loose scalar/enum rules,
// materializes nested defaults, and canonicalizes every list to []any in one pass.
func coerceVariableValue(schema *ast.Schema, typ *ast.Type, value any, path ast.Path) (any, *gqlerror.Error) {
	if value == nil {
		if typ.NonNull {
			return nil, gqlerror.ErrorPathf(path, "cannot be null")
		}
		return nil, nil
	}
	if typ.Elem != nil {
		rv := reflect.ValueOf(value)
		if rv.Kind() != reflect.Array && rv.Kind() != reflect.Slice {
			item, err := coerceVariableValue(schema, typ.Elem, value, append(path, ast.PathIndex(0)))
			return []any{item}, err
		}
		list := make([]any, rv.Len())
		for i := range list {
			item, err := coerceVariableValue(schema, typ.Elem, rv.Index(i).Interface(), append(path, ast.PathIndex(i)))
			if err != nil {
				return nil, err
			}
			list[i] = item
		}
		return list, nil
	}
	def := schema.Types[typ.NamedType]
	if def == nil {
		return nil, gqlerror.ErrorPathf(path, "unknown type %s", typ.NamedType)
	}
	switch def.Kind {
	case ast.Enum:
		s, ok := value.(string)
		if !ok {
			return nil, gqlerror.ErrorPathf(path, "cannot use %T as %s", value, typ.NamedType)
		}
		for _, item := range def.EnumValues {
			if item.Name == s {
				return value, nil
			}
		}
		return nil, gqlerror.ErrorPathf(path, "%s is not a valid %s", s, typ.NamedType)
	case ast.InputObject:
		m, ok := value.(map[string]any)
		if !ok {
			return nil, gqlerror.ErrorPathf(path, "must be a %s, not a %T", def.Name, value)
		}
		for name := range m {
			if def.Fields.ForName(name) == nil {
				return nil, gqlerror.ErrorPathf(append(path, ast.PathName(name)), "unknown field")
			}
		}
		if def.Directives.ForName("oneOf") != nil {
			if len(m) != 1 {
				return nil, gqlerror.ErrorPathf(path, "must specify exactly one field")
			}
			for name, value := range m {
				if value == nil {
					return nil, gqlerror.ErrorPathf(append(path, ast.PathName(name)), "cannot be null")
				}
			}
		}
		for _, fd := range def.Fields {
			field, present := m[fd.Name]
			if !present && fd.DefaultValue != nil {
				var err error
				field, err = fd.DefaultValue.Value(nil)
				if err != nil {
					return nil, gqlerror.WrapPath(append(path, ast.PathName(fd.Name)), err)
				}
				present = true
			}
			if !present {
				if fd.Type.NonNull {
					return nil, gqlerror.ErrorPathf(append(path, ast.PathName(fd.Name)), "must be defined")
				}
				continue
			}
			field, strictErr := coerceVariableValue(schema, fd.Type, field, append(path, ast.PathName(fd.Name)))
			if strictErr != nil {
				return nil, strictErr
			}
			m[fd.Name] = field
		}
		return m, nil
	case ast.Scalar:
		return coerceScalar(typ.NamedType, value, path)
	default:
		return value, nil
	}
}

func coerceScalar(name string, value any, path ast.Path) (any, *gqlerror.Error) {
	if name == "BigInt" || name == "Decimal" {
		text, err := exactNumber(name, value)
		if err != nil {
			return nil, gqlerror.WrapPath(path, err)
		}
		return text, nil
	}
	if name == "Int" {
		if n, ok := value.(stdjson.Number); ok {
			i, err := strconv.ParseInt(string(n), 10, 32)
			if err != nil {
				return nil, gqlerror.ErrorPathf(path, "cannot use %s as Int", n)
			}
			return i, nil
		}
		if !validGraphQLInt(value) {
			return nil, gqlerror.ErrorPathf(path, "cannot use %T as Int", value)
		}
		return value, nil
	}
	if name == "Float" {
		if n, ok := value.(stdjson.Number); ok {
			f, err := strconv.ParseFloat(string(n), 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				return nil, gqlerror.ErrorPathf(path, "cannot use %s as Float", n)
			}
			return f, nil
		}
		switch v := value.(type) {
		case float32, float64:
			if math.IsNaN(float64(reflect.ValueOf(v).Float())) || math.IsInf(float64(reflect.ValueOf(v).Float()), 0) {
				return nil, gqlerror.ErrorPathf(path, "cannot use %T as Float", value)
			}
			return value, nil
		default:
			if isSignedInteger(value) {
				return value, nil
			}
		}
		return nil, gqlerror.ErrorPathf(path, "cannot use %T as Float", value)
	}
	switch name {
	case "Boolean":
		if _, ok := value.(bool); !ok {
			return nil, gqlerror.ErrorPathf(path, "cannot use %T as Boolean", value)
		}
	case "String":
		if _, ok := value.(string); !ok {
			return nil, gqlerror.ErrorPathf(path, "cannot use %T as String", value)
		}
	case "ID":
		if _, ok := value.(string); !ok && !isSignedInteger(value) && !isJSONInteger(value) {
			return nil, gqlerror.ErrorPathf(path, "cannot use %T as ID", value)
		}
	}
	return value, nil
}

func isSignedInteger(v any) bool {
	if _, ok := v.(stdjson.Number); ok {
		return false
	}
	rv := reflect.ValueOf(v)
	return rv.IsValid() && (rv.Kind() >= reflect.Int && rv.Kind() <= reflect.Int64 || rv.Kind() >= reflect.Uint && rv.Kind() <= reflect.Uint64)
}

func validGraphQLInt(v any) bool {
	if !isSignedInteger(v) {
		return false
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() >= reflect.Uint && rv.Kind() <= reflect.Uint64 {
		return rv.Uint() <= math.MaxInt32
	}
	i := rv.Int()
	return i >= math.MinInt32 && i <= math.MaxInt32
}

func isJSONInteger(v any) bool {
	n, ok := v.(stdjson.Number)
	if !ok {
		return false
	}
	_, err := strconv.ParseInt(string(n), 10, 64)
	return err == nil
}
