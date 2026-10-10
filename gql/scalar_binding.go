package gql

import (
	"encoding"
	stdjson "encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
)

type scalarBinding struct {
	nullable, fast, number bool
}

// 普通标量直接赋值/解析；自定义 JSON 类型与复合标量使用原有 JSON 契约。
func (my scalarBinding) decode(target reflect.Value, raw any) error {
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

func (my scalarBinding) encode(value reflect.Value) (any, error) {
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
	if !value.CanAddr() {
		copy := reflect.New(value.Type()).Elem()
		copy.Set(value)
		value = copy
	}
	data, err := json.Marshal(value.Addr().Interface())
	if err != nil {
		return nil, err
	}
	var output any
	err = jsonNumeric.Unmarshal(data, &output)
	return output, err
}

func newScalarBinding(t reflect.Type, nullable bool) scalarBinding {
	t = deref(t)
	fast := t.Kind() >= reflect.Bool && t.Kind() <= reflect.Uint64 || t.Kind() == reflect.Float32 || t.Kind() == reflect.Float64 || t.Kind() == reflect.String
	ptr := reflect.PointerTo(t)
	unmarshal := ptr.Implements(reflect.TypeFor[stdjson.Unmarshaler]()) || ptr.Implements(reflect.TypeFor[encoding.TextUnmarshaler]())
	marshal := ptr.Implements(reflect.TypeFor[stdjson.Marshaler]()) || ptr.Implements(reflect.TypeFor[encoding.TextMarshaler]())
	return scalarBinding{nullable, fast && !unmarshal && !marshal, t.Kind() >= reflect.Int && t.Kind() <= reflect.Uint64 && !unmarshal}
}
