package gql

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
)

// 参数树只在注册期编译。请求仅按字段索引读写，保留省略/null/零值。
type inputBinding struct {
	scalar   *scalarBinding
	fields   []boundInputField
	elem     *inputBinding
	nullable bool
	id       bool
}

type boundInputField struct {
	name, goName string
	index        int
	plan         *inputBinding
}

func (my *Engine) bindInput(t reflect.Type, typ *ast.Type, fields ast.FieldList, parents ...reflect.Type) (*inputBinding, error) {
	plan := &inputBinding{nullable: typ == nil || !typ.NonNull}
	base := deref(t)
	if typ != nil && typ.Elem != nil {
		if base.Kind() != reflect.Slice {
			return nil, fmt.Errorf("%s必须绑定列表", typ)
		}
		var err error
		plan.elem, err = my.bindInput(base.Elem(), typ.Elem, nil, parents...)
		return plan, err
	}
	if typ != nil {
		kind := my.schema.Types[typ.Name()]
		if kind == nil {
			return nil, fmt.Errorf("未知输入类型: %s", typ)
		}
		if kind.Kind == ast.Scalar || kind.Kind == ast.Enum || base.Kind() == reflect.Map {
			goKind := deriveType(t, true)
			if kind.Kind == ast.Scalar && goKind != kind.Name && !(kind.Name == "ID" && (goKind == "String" || goKind == "BigInt")) && !(kind.Name == "BigInt" && goKind == "Int") {
				return nil, fmt.Errorf("输入类型不匹配: %s / %s", goKind, typ)
			}
			codec := newScalarBinding(t, plan.nullable)
			plan.scalar = &codec
			plan.id = kind.Name == "ID" && base.Kind() == reflect.String
			return plan, nil
		}
		fields = kind.Fields
	}
	if base.Kind() != reflect.Struct {
		return nil, fmt.Errorf("输入对象必须绑定struct: %s", t)
	}
	if slices.Contains(parents, base) {
		return nil, fmt.Errorf("原生字段绑定必须使用有限的输入/输出结构: %s", base)
	}
	parents = append(parents, base)
	var failure error
	seen := map[string]bool{}
	eachField(base, func(f reflect.StructField, name, _ string) {
		if failure != nil {
			return
		}
		field := fields.ForName(name)
		if field == nil || f.Anonymous || seen[name] {
			failure = fmt.Errorf("输入字段不存在、重复或为嵌入字段: %s", name)
			return
		}
		seen[name] = true
		child, err := my.bindInput(f.Type, field.Type, nil, parents...)
		if err != nil {
			failure = fmt.Errorf("%s: %w", name, err)
			return
		}
		plan.fields = append(plan.fields, boundInputField{name, f.Name, f.Index[0], child})
	})
	return plan, failure
}

func inputPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// children 按绑定字段或列表行索引排列，标量只占一个存在位；路径仅在Has/Include时解析。
type inputState struct {
	present  bool
	children []inputState
}

func (my *inputState) child(i int) *inputState {
	if my == nil {
		return nil
	}
	return &my.children[i]
}

func (my *inputBinding) lookup(path string, state *inputState, include bool) *inputState {
	name, rest, nested := strings.Cut(path, ".")
	base, index, indexed := strings.Cut(name, "[")
	if state == nil {
		return nil
	}
	if len(state.children) == 0 {
		if !include {
			return nil
		}
		state.children = make([]inputState, len(my.fields))
	}
	for i, field := range my.fields {
		if field.name != base {
			continue
		}
		plan, child := field.plan, &state.children[i]
		parent := child
		if indexed {
			row, err := strconv.Atoi(strings.TrimSuffix(index, "]"))
			if err != nil || row < 0 || !strings.HasSuffix(index, "]") || plan.elem == nil || row >= len(child.children) {
				return nil
			}
			plan, child = plan.elem, &child.children[row]
		}
		result := child
		if nested {
			result = plan.lookup(rest, child, include)
		}
		if include && result != nil {
			parent.present = true
			child.present = true
			result.present = true
		}
		return result
	}
	return nil
}

func (my *inputBinding) decode(target reflect.Value, raw any, state *inputState) error {
	if state != nil {
		state.present = true
	}
	if my.scalar != nil {
		if my.id && raw != nil {
			raw = fmt.Sprint(raw)
		}
		return my.scalar.decode(target, raw)
	}
	if raw == nil {
		if !my.nullable || target.Kind() != reflect.Pointer && target.Kind() != reflect.Slice {
			return fmt.Errorf("不能为null")
		}
		return nil
	}
	for target.Kind() == reflect.Pointer {
		target.Set(reflect.New(target.Type().Elem()))
		target = target.Elem()
	}
	if my.elem != nil {
		rows, ok := raw.([]any)
		if !ok {
			return fmt.Errorf("不是列表")
		}
		target.Set(reflect.MakeSlice(target.Type(), len(rows), len(rows)))
		if state != nil {
			state.children = make([]inputState, len(rows))
		}
		for i, row := range rows {
			if err := my.elem.decode(target.Index(i), row, state.child(i)); err != nil {
				return fmt.Errorf("[%d]: %w", i, err)
			}
		}
		return nil
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("不是对象")
	}
	if state != nil {
		state.children = make([]inputState, len(my.fields))
	}
	for i, field := range my.fields {
		if value, exists := object[field.name]; exists {
			if err := field.plan.decode(target.Field(field.index), value, state.child(i)); err != nil {
				return fmt.Errorf("%s: %w", field.name, err)
			}
		}
	}
	return nil
}

// state 为 nil 时编码完整返回值；输入按提交状态合并，保留未绑定字段。
func (my *inputBinding) encode(value reflect.Value, raw any, state *inputState) (any, error) {
	if my.scalar != nil {
		out, err := my.scalar.encode(value)
		if err == nil && out == nil && state != nil && !my.nullable {
			err = fmt.Errorf("不能为null")
		}
		return out, err
	}
	for value.Kind() == reflect.Pointer || value.Kind() == reflect.Slice {
		if value.IsNil() {
			if state != nil && !my.nullable {
				return nil, fmt.Errorf("不能为null")
			}
			return nil, nil
		}
		if value.Kind() == reflect.Slice {
			break
		}
		value = value.Elem()
	}
	if my.elem != nil {
		previous, _ := raw.([]any)
		if state != nil && len(previous) != value.Len() {
			return nil, fmt.Errorf("不允许改变批量输入行数")
		}
		if state == nil || previous == nil {
			previous = make([]any, value.Len())
		}
		for i := range previous {
			out, err := my.elem.encode(value.Index(i), previous[i], state.child(i))
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			previous[i] = out
		}
		return previous, nil
	}
	object, _ := raw.(map[string]any)
	if object == nil {
		object = make(map[string]any, len(my.fields))
	}
	for i, field := range my.fields {
		if state != nil && (len(state.children) == 0 || !state.children[i].present) {
			continue
		}
		out, err := field.plan.encode(value.Field(field.index), object[field.name], state.child(i))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field.name, err)
		}
		object[field.name] = out
	}
	return object, nil
}

func (my *inputBinding) selectFields(value reflect.Value, goPath string, state *inputState, selected *[]string) {
	if my.scalar != nil {
		*selected = append(*selected, goPath)
		return
	}
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			*selected = append(*selected, goPath)
			return
		}
		value = value.Elem()
	}
	if my.elem != nil {
		// 对PATCH中的列表整体赋值，validator的dive负责元素规则。
		*selected = append(*selected, goPath)
		return
	}
	for i, field := range my.fields {
		if len(state.children) > 0 && state.children[i].present {
			field.plan.selectFields(value.Field(field.index), inputPath(goPath, field.goName), state.child(i), selected)
		}
	}
}
