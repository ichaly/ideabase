package gql

import (
	"context"
	"encoding"
	stdjson "encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2/ast"
)

// Change 是一个原生 mutation 输入行的强类型视图。批量创建逐行处理，
// 批量更新处理一次共享 patch；Arguments 是不含 input 的根参数副本。
// Input 不包含数据库旧值。只有 Input 的已提交字段和 Include 字段会写回。
type Change[I any] struct {
	MutationRequest
	Input   I
	plan    *mutationInput
	present []bool
	err     error
}

// Has 区分省略与显式提交（包括 null、false 和 0），使用 GraphQL 字段名。
func (my *Change[I]) Has(name string) bool {
	index, ok := my.plan.index[name]
	return ok && my.present[index]
}

// Include 将已赋值的 Input 字段加入写入集，未知字段会阻止 SQL 执行。
func (my *Change[I]) Include(names ...string) {
	for _, name := range names {
		index, ok := my.plan.index[name]
		if !ok {
			my.err = fmt.Errorf("mutation未绑定字段: %s", name)
			return
		}
		my.present[index] = true
	}
}

// NewMutation 为原生 create/update/upsert 绑定结构体，不重定义 schema 或 SQL。
// 可选 prepare 在类型解码后、标签校验前执行，最多一个；返回成功后引擎自动继续。
// 创建/upsert 校验全部字段，update 只校验提交或 Include 的字段。
func NewMutation[I any](class string, prepare ...func(context.Context, *Change[I]) error) Mutation {
	if len(prepare) > 1 {
		panic("NewMutation最多声明一个输入处理器")
	}
	var fn func(context.Context, *Change[I]) error
	if len(prepare) == 1 {
		fn = prepare[0]
	}
	return Mutation{Class: class, inputType: reflect.TypeFor[I](), bind: func(ctx context.Context, request MutationRequest, rows []map[string]any, plan *mutationInput) error {
		v, err := inputValidator(ctx)
		if err != nil {
			return err
		}
		if fn != nil {
			// 处理器只能通过 Input 修改写入内容，不能替换未经校验的 input 或修改 SQL 目标。
			args := make(map[string]any, len(request.Arguments))
			for name, value := range request.Arguments {
				if name != protocol.INPUT {
					args[name] = cloneVariableValue(value)
				}
			}
			request.Arguments = args
		}
		for _, row := range rows {
			change := Change[I]{MutationRequest: request, plan: plan, present: make([]bool, len(plan.fields))}
			value := reflect.ValueOf(&change.Input).Elem()
			for i, field := range plan.fields {
				raw, present := row[field.name]
				if !present {
					continue
				}
				change.present[i] = true
				if err := field.decode(value.Field(field.index), raw); err != nil {
					return &publicFailure{fmt.Sprintf("%s.%s类型、空值或数值范围无效", class, field.name), "VALIDATION_ERROR"}
				}
			}
			if fn != nil {
				if err := fn(ctx, &change); err != nil {
					return err
				}
				if change.err != nil {
					return change.err
				}
			}
			if request.Operation == protocol.UPDATE {
				selected := make([]string, 0, len(row))
				for i, field := range plan.fields {
					if change.present[i] {
						selected = append(selected, field.goName)
					}
				}
				err = v.StructPartialCtx(ctx, &change.Input, selected...)
			} else {
				err = v.StructCtx(ctx, &change.Input)
			}
			if err != nil {
				return inputValidationError(err)
			}
			for i, field := range plan.fields {
				if !change.present[i] {
					continue
				}
				output, err := field.encode(value.Field(field.index))
				if err != nil {
					return err
				}
				if output == nil && !field.nullable {
					return &publicFailure{fmt.Sprintf("%s.%s不能为null", class, field.name), "VALIDATION_ERROR"}
				}
				row[field.name] = output
			}
		}
		return nil
	}}
}

type mutationInput struct {
	fields []mutationInputField
	index  map[string]int
}

type mutationInputField struct {
	name, goName           string
	index                  int
	nullable, fast, number bool
}

// 普通标量直接赋值/解析；自定义 JSON 类型与复合标量使用原有 JSON 契约。
func (my mutationInputField) decode(target reflect.Value, raw any) error {
	if raw == nil {
		if target.Kind() != reflect.Pointer || !my.nullable {
			return fmt.Errorf("null")
		}
		return nil
	}
	if !my.fast {
		input := resolverNumericInput(target.Type(), raw)
		if text, ok := raw.(string); ok && my.number {
			input = stdjson.Number(text)
		}
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, target.Addr().Interface())
	}
	for target.Kind() == reflect.Pointer {
		target.Set(reflect.New(target.Type().Elem()))
		target = target.Elem()
	}
	source := reflect.ValueOf(raw)
	if source.Type() == target.Type() {
		target.Set(source)
		return nil
	}
	if source.Kind() == target.Kind() {
		target.Set(source.Convert(target.Type()))
		return nil
	}
	// GraphQL 已校验标量语法；这里补充 Go 目标位宽检查，不经过 float64 转换整数。
	text, ok := raw.(string)
	if !ok {
		text = fmt.Sprint(raw)
	}
	switch target.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(text, 10, target.Type().Bits())
		if err != nil {
			return err
		}
		target.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(text, 10, target.Type().Bits())
		if err != nil {
			return err
		}
		target.SetUint(n)
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(text, target.Type().Bits())
		if err != nil {
			return err
		}
		target.SetFloat(n)
	default:
		return fmt.Errorf("标量类型不匹配")
	}
	return nil
}

func (my mutationInputField) encode(value reflect.Value) (any, error) {
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil, nil
		}
		value = value.Elem()
	}
	if my.fast {
		if (value.Kind() == reflect.Float32 || value.Kind() == reflect.Float64) && (math.IsInf(value.Float(), 0) || math.IsNaN(value.Float())) {
			return nil, fmt.Errorf("mutation数值必须有限")
		}
		// 去掉业务别名类型，保持后续编译器与策略看到的标准标量类型。
		switch value.Kind() {
		case reflect.String:
			return value.String(), nil
		case reflect.Bool:
			return value.Bool(), nil
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return value.Int(), nil
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return value.Uint(), nil
		case reflect.Float32:
			return float32(value.Float()), nil
		case reflect.Float64:
			return value.Float(), nil
		}
		return value.Interface(), nil
	}
	data, err := json.Marshal(value.Addr().Interface())
	if err != nil {
		return nil, err
	}
	var output any
	err = jsonNumeric.Unmarshal(data, &output)
	return output, err
}

func newMutationInputField(f reflect.StructField, name string, nullable bool) mutationInputField {
	t := deref(f.Type)
	fast := t.Kind() >= reflect.Bool && t.Kind() <= reflect.Uint64 || t.Kind() == reflect.Float32 || t.Kind() == reflect.Float64 || t.Kind() == reflect.String
	ptr := reflect.PointerTo(t)
	unmarshal := ptr.Implements(reflect.TypeFor[stdjson.Unmarshaler]()) || ptr.Implements(reflect.TypeFor[encoding.TextUnmarshaler]())
	marshal := ptr.Implements(reflect.TypeFor[stdjson.Marshaler]()) || ptr.Implements(reflect.TypeFor[encoding.TextMarshaler]())
	return mutationInputField{name, f.Name, f.Index[0], nullable, fast && !unmarshal && !marshal, t.Kind() >= reflect.Int && t.Kind() <= reflect.Uint64 && !unmarshal}
}

func (my *Engine) mutationInputFields(item Mutation, class *protocol.Class) (*mutationInput, error) {
	if item.inputType == nil {
		return nil, nil
	}
	if item.inputType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("mutation输入必须是struct: %s", item.Class)
	}
	plan := &mutationInput{index: make(map[string]int)}
	var failure error
	renderer := NewRenderer(my.metadata)
	eachField(item.inputType, func(field reflect.StructField, name, _ string) {
		column := class.Fields[name]
		_, duplicate := plan.index[name]
		if field.Anonymous || column == nil || column.Column == "" || column.ReadOnly || duplicate {
			failure = fmt.Errorf("mutation输入字段不存在、重复或不可写: %s.%s", item.Class, name)
			return
		}
		kind, goKind := renderer.getGraphQLType(column), deriveType(field.Type, true)
		compatible := kind == goKind || kind == "BigInt" && goKind == "Int" || kind == "ID" && (goKind == "String" || goKind == "BigInt")
		if scalar := my.schema.Types[kind]; scalar == nil || scalar.Kind != ast.Scalar || !compatible {
			failure = fmt.Errorf("mutation输入类型不匹配: %s.%s (%s / %s)", item.Class, name, goKind, kind)
			return
		}
		plan.index[name] = len(plan.fields)
		plan.fields = append(plan.fields, newMutationInputField(field, name, column.Nullable))
	})
	return plan, failure
}
