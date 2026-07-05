// 注册即声明：Action的schema形状从Go函数签名反射推导（启动期一次），
// 执行期入参按json标签解码进强类型struct并校验validate标签，
// 业务侧不写SDL/yaml、不写解码校验样板
package gql

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/iancoleman/strcase"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/std"
)

// NewAction 从fn签名构造Action：I的字段→参数（json定名/doc作描述/validate含
// required则非空!），O→结果类型。缺省挂Mutation根，Option按需覆盖。
// 无参Action的I用struct{}；I非struct在注册期直接panic（启动即失败）
func NewAction[I, O any](name, doc string, fn func(context.Context, I) (O, error), opts ...Option) Action {
	define := Define{
		Name:   name,
		Doc:    doc,
		Args:   deriveArgs(reflect.TypeFor[I]()),
		Result: deriveResult(reflect.TypeFor[O]()),
	}
	for _, opt := range opts {
		opt(&define)
	}
	return &derived[I, O]{define: define, fn: fn}
}

// Option 微调反射推导的声明
type Option func(*Define)

// Result 覆盖结果类型：Go类型名与元数据实体名不一致时用（如 ent.Profile → BotProfile）
func Result(name string) Option { return func(my *Define) { my.Result = name } }

// Query 挂载到Query根（缺省Mutation：Action多为变更编排）
func Query() Option { return func(my *Define) { my.Query = true } }

// Extra 附加SDL（辅助input/type声明），原样并入schema
func Extra(sdl string) Option { return func(my *Define) { my.Extra = sdl } }

// derived 反射装配的Action实现
type derived[I, O any] struct {
	define Define
	fn     func(context.Context, I) (O, error)
}

func (my *derived[I, O]) Define() Define { return my.define }

func (my *derived[I, O]) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	in, err := decode[I](args)
	if err != nil {
		return nil, err
	}
	out, err := my.fn(ctx, in)
	if err != nil {
		return nil, err
	}
	return erase(out), nil
}

// decode 入参json往返解码进强类型struct并校验validate标签
func decode[I any](args map[string]interface{}) (I, error) {
	var in I
	data, err := json.Marshal(args)
	if err == nil {
		err = json.Unmarshal(data, &in)
	}
	if err == nil {
		err = check(&in)
	}
	return in, err
}

// erase 擦除类型化空指针为真nil（回查与输出按nil处理）
func erase(out any) any {
	if v := reflect.ValueOf(out); v.Kind() == reflect.Ptr && v.IsNil() {
		return nil
	}
	return out
}

// Source 宿主行：SQL查出的已选字段（形状由客户端选择集决定，故保持map）
type Source = map[string]interface{}

// NewResolver 注册即声明的字段级解析器：挂载为class实体的虚拟字段，
// args反射自I（同NewAction规则），字段类型反射自O；注册键为 class.name
func NewResolver[I, O any](class, name, doc string, fn func(context.Context, Source, I) (O, error)) Resolver {
	return &single[I, O]{define: defineField[I, O](class, name, doc), fn: fn}
}

// NewBatch 批量版NewResolver：整结果集一次调用免N+1，返回值须与sources等长对位
func NewBatch[I, O any](class, name, doc string, fn func(context.Context, []Source, I) ([]O, error)) Resolver {
	return &batch[I, O]{define: defineField[I, O](class, name, doc), fn: fn}
}

// defineField 字段级声明：Class非空使sdl()渲染为 extend type <Class>
func defineField[I, O any](class, name, doc string) Define {
	return Define{
		Class:  class,
		Name:   name,
		Doc:    doc,
		Args:   deriveArgs(reflect.TypeFor[I]()),
		Result: deriveResult(reflect.TypeFor[O]()),
	}
}

// single 单对象解析器（NewResolver反射装配）
type single[I, O any] struct {
	define Define
	fn     func(context.Context, Source, I) (O, error)
}

func (my *single[I, O]) Name() string           { return my.define.Class + "." + my.define.Name }
func (my *single[I, O]) Define() Define         { return my.define }
func (my *single[I, O]) field() *protocol.Field { return &protocol.Field{Resolver: my.Name()} }

func (my *single[I, O]) Resolve(ctx context.Context, source, args map[string]interface{}) (interface{}, error) {
	in, err := decode[I](args)
	if err != nil {
		return nil, err
	}
	out, err := my.fn(ctx, source, in)
	if err != nil {
		return nil, err
	}
	return erase(out), nil
}

// batch 批量解析器（NewBatch反射装配）
type batch[I, O any] struct {
	define Define
	fn     func(context.Context, []Source, I) ([]O, error)
}

func (my *batch[I, O]) Name() string           { return my.define.Class + "." + my.define.Name }
func (my *batch[I, O]) Define() Define         { return my.define }
func (my *batch[I, O]) field() *protocol.Field { return &protocol.Field{Resolver: my.Name()} }

func (my *batch[I, O]) Resolve(context.Context, map[string]interface{}, map[string]interface{}) (interface{}, error) {
	return nil, fmt.Errorf("批量解析器不支持单对象路径")
}

func (my *batch[I, O]) ResolveBatch(ctx context.Context, sources []map[string]interface{}, args map[string]interface{}) ([]interface{}, error) {
	in, err := decode[I](args)
	if err != nil {
		return nil, err
	}
	outs, err := my.fn(ctx, sources, in)
	if err != nil {
		return nil, err
	}
	values := make([]interface{}, len(outs))
	for i, out := range outs {
		values[i] = erase(out)
	}
	return values, nil
}

// NewRemote 注册即声明的远程关系：挂载为class实体的虚拟字段，key为宿主键字段名；
// O为命名struct时自动反射出虚拟类型SDL（多个远程共享同一类型时重建期按文本去重）
func NewRemote[O any](class, name, doc, key string, fn func(context.Context, []any) (map[any]O, error)) Remote {
	t := reflect.TypeFor[O]()
	return &distant[O]{
		define: Define{Class: class, Name: name, Doc: doc, Result: deriveResult(t), Extra: deriveObject(t)},
		key:    key,
		fn:     fn,
	}
}

// distant 远程数据源（NewRemote反射装配）
type distant[O any] struct {
	define Define
	key    string
	fn     func(context.Context, []any) (map[any]O, error)
}

func (my *distant[O]) Name() string   { return my.define.Class + "." + my.define.Name }
func (my *distant[O]) Define() Define { return my.define }
func (my *distant[O]) field() *protocol.Field {
	return &protocol.Field{Remote: &protocol.RemoteRef{Source: my.Name(), Key: my.key}}
}

func (my *distant[O]) Fetch(ctx context.Context, keys []any) (map[any]any, error) {
	values, err := my.fn(ctx, keys)
	if err != nil {
		return nil, err
	}
	out := make(map[any]any, len(values))
	for k, v := range values {
		out[k] = plain(v) // 归一为json形态：程序化Data与HTTP输出一致，json标签生效
	}
	return out, nil
}

// plain 值经json往返归一为通用形态（map/切片/标量）；失败原样返回
func plain(v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if json.Unmarshal(data, &out) != nil {
		return v
	}
	return out
}

// deriveObject 反射命名struct为GraphQL输出类型SDL（远程虚拟类型）；
// 标量特化类型与非命名struct返回空（结果为标量/Json，无需辅助类型）
func deriveObject(t reflect.Type) string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if _, ok := scalars[t]; ok || t.Kind() != reflect.Struct || t.Name() == "" {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "type %s {\n", t.Name())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if !f.IsExported() || name == "-" {
			continue
		}
		if name == "" {
			name = strcase.ToLowerCamel(f.Name)
		}
		if doc := f.Tag.Get("doc"); doc != "" {
			fmt.Fprintf(&sb, "  \"\"\"%s\"\"\"\n", doc)
		}
		fmt.Fprintf(&sb, "  %s: %s\n", name, deriveScalar(f.Type))
	}
	sb.WriteString("}")
	return sb.String()
}

// check validate标签校验：与REST绑定路径同款std.Validator（含去空格与中文翻译）
var validator = sync.OnceValues(std.NewValidator)

func check(v any) error {
	c, err := validator()
	if err != nil {
		return err
	}
	return c.Struct(v)
}

// deriveArgs 反射struct字段为GraphQL参数签名文本
func deriveArgs(t reflect.Type) string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("Action入参必须是struct（无参用struct{}），收到 %s", t))
	}
	var parts []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if !f.IsExported() || name == "-" {
			continue
		}
		if name == "" {
			name = strcase.ToLowerCamel(f.Name)
		}
		kind := deriveScalar(f.Type)
		if required(f.Tag.Get("validate")) {
			kind += "!"
		}
		part := name + ": " + kind
		if doc := f.Tag.Get("doc"); doc != "" {
			part = `"` + doc + `" ` + part
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

// required validate标签是否含独立的required规则（required_if等条件规则不算）
func required(tag string) bool {
	for _, rule := range strings.Split(tag, ",") {
		if rule == "required" {
			return true
		}
	}
	return false
}

// deriveScalar Go类型→GraphQL入参类型：指针剥离（可空由required缺失表达），
// 切片元素非指针补!，map与嵌套struct视作Json透传
func deriveScalar(t reflect.Type) string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if name, ok := scalars[t]; ok {
		return name
	}
	switch t.Kind() {
	case reflect.String:
		return "String"
	case reflect.Bool:
		return "Boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "Int"
	case reflect.Float32, reflect.Float64:
		return "Float"
	case reflect.Slice, reflect.Array:
		bang := "!"
		if t.Elem().Kind() == reflect.Ptr {
			bang = ""
		}
		return "[" + deriveScalar(t.Elem()) + bang + "]"
	default: // map与struct等复合形态按Json透传（json往返解码天然支持）
		return protocol.SCALAR_JSON
	}
}

// deriveResult Go类型→GraphQL结果类型：struct取类型名（与元数据实体同名即触发
// 回查补全，不一致用Result选项覆盖），其余同入参规则
func deriveResult(t reflect.Type) string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() == reflect.Struct {
		if name, ok := scalars[t]; ok {
			return name
		}
		return t.Name()
	}
	return deriveScalar(t)
}

// scalars 特化标量映射（先于Kind分派）
var scalars = map[reflect.Type]string{
	reflect.TypeFor[std.Id](): "ID",
	reflect.TypeFor[time.Time](): protocol.SCALAR_DATE_TIME,
}
