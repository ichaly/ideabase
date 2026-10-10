package gql

import (
	stdjson "encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	gqlvalidator "github.com/vektah/gqlparser/v2/validator"
)

// numericCodec 默认将精确数值输出为字符串；数据库值及输入文本不经过浮点数。
// ID 也使用字符串输出，WithCodecs 同名注册仍可覆盖（例如 NewIdCodec 短码）。
type numericCodec string

func (my numericCodec) Name() string { return string(my) }

func (my numericCodec) Encode(token []byte) []byte {
	if len(token) == 0 || !(token[0] == '-' || token[0] >= '0' && token[0] <= '9') {
		return nil
	}
	return append(append([]byte{'"'}, token...), '"')
}

func (my numericCodec) Decode(value any) any { return value }

// exactNumber 保留字符串和 JSON number 的十进制原文；不接收已可能舍入的浮点数。
// BigInt 不限定为 int64，数据库列或业务 resolver 决定具体范围。
func exactNumber(name string, value any) (string, error) {
	var text string
	switch v := value.(type) {
	case string:
		text = v
	case stdjson.Number:
		text = string(v)
	default:
		if !isSignedInteger(value) {
			return "", fmt.Errorf("%s requires a decimal string or an exact integer", name)
		}
		rv := reflect.ValueOf(value)
		if rv.Kind() >= reflect.Uint && rv.Kind() <= reflect.Uint64 {
			text = strconv.FormatUint(rv.Uint(), 10)
		} else {
			text = strconv.FormatInt(rv.Int(), 10)
		}
	}
	if len(text) == 0 || strings.TrimSpace(text) != text || !(text[0] == '-' || text[0] >= '0' && text[0] <= '9') || !stdjson.Valid([]byte(text)) || name == protocol.SCALAR_BIG_INT && strings.ContainsAny(text, ".eE") {
		return "", fmt.Errorf("invalid %s decimal value", name)
	}
	return text, nil
}

// resolverNumericInput 只把目标为 64 位整数的字符串变为 JSON number，
// 再由强类型 JSON 解码器检查有符号/无符号范围；不改变字符串业务字段。
func resolverNumericInput(target reflect.Type, value any) any {
	if value == nil {
		return nil
	}
	target = deref(target)
	switch target.Kind() {
	case reflect.Int64, reflect.Uint64:
		// std.Id 等自定义 JSON 类型保留自己的解码规则。
		if reflect.PointerTo(target).Implements(reflect.TypeFor[stdjson.Unmarshaler]()) {
			return value
		}
		if text, ok := value.(string); ok {
			if _, err := exactNumber(protocol.SCALAR_BIG_INT, text); err == nil {
				return stdjson.Number(text)
			}
		}
	case reflect.Struct:
		if object, ok := value.(map[string]any); ok {
			out := make(map[string]any, len(object))
			for name, field := range object {
				out[name] = field
			}
			eachField(target, func(field reflect.StructField, name, _ string) {
				if item, exists := out[name]; exists {
					out[name] = resolverNumericInput(field.Type, item)
				}
			})
			return out
		}
	case reflect.Slice, reflect.Array:
		if list, ok := value.([]any); ok {
			out := make([]any, len(list))
			for i, item := range list {
				out[i] = resolverNumericInput(target.Elem(), item)
			}
			return out
		}
	}
	return value
}

// normalizeExactLiterals 在 AST 转为 Go 值之前保留数值原文，包含变量默认值和列表。
func normalizeExactLiterals(schema *ast.Schema, doc *ast.QueryDocument) gqlerror.List {
	var errs gqlerror.List
	events := &gqlvalidator.Events{}
	events.OnValue(func(_ *gqlvalidator.Walker, value *ast.Value) {
		if value.ExpectedType == nil || value.Kind == ast.Variable || value.Kind == ast.NullValue {
			return
		}
		if value.Kind == ast.ListValue && value.ExpectedType.Elem != nil {
			return
		}
		name := value.ExpectedType.Name()
		if name != protocol.SCALAR_BIG_INT && name != protocol.SCALAR_DECIMAL {
			return
		}
		if value.Kind != ast.IntValue && value.Kind != ast.FloatValue && value.Kind != ast.StringValue && value.Kind != ast.BlockValue {
			errs = append(errs, gqlerror.ErrorPosf(value.Position, "invalid %s value", name))
			return
		}
		if _, err := exactNumber(name, value.Raw); err != nil {
			errs = append(errs, gqlerror.ErrorPosf(value.Position, "%s", err))
			return
		}
		value.Kind = ast.StringValue
	})
	gqlvalidator.Walk(schema, doc, events)
	return errs
}
