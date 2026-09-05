// 注册即声明：Resolver的schema形状从Go函数签名反射推导（启动期一次），
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

// Option 微调反射推导的声明
type Option func(*Define)

// SemanticKind describes the business meaning of a custom handler for
// agent/tooling discovery. It is independent from the execution HandlerKind.
type SemanticKind string

const (
	SemanticPrimitive  SemanticKind = "primitive"
	SemanticProjection SemanticKind = "projection"
	SemanticCommand    SemanticKind = "command"
	SemanticWorkflow   SemanticKind = "workflow"
)

// Semantic explicitly overrides the location-derived semantic kind.
func Semantic(kind SemanticKind) Option { return func(my *Define) { my.SemanticKind = kind } }

func deriveSemanticKind(define Define) SemanticKind {
	if define.SemanticKind != "" {
		return define.SemanticKind
	}
	if define.Class == "Mutation" {
		return SemanticCommand
	}
	if define.Class == "Query" || define.Class == "Subscription" {
		return SemanticPrimitive
	}
	return SemanticProjection
}

func validSemanticKind(kind SemanticKind) bool {
	return kind == SemanticPrimitive || kind == SemanticProjection || kind == SemanticCommand || kind == SemanticWorkflow
}

// Result 覆盖结果类型：Go类型名与元数据实体名不一致时用（如 ent.Profile → BotProfile）
func Result(name string) Option { return func(my *Define) { my.Result = name } }

// Extra 附加SDL（辅助input/type声明），原样并入schema
func Extra(sdl string) Option { return func(my *Define) { my.Extra = sdl } }

// Cost 声明字段的单次解析权重，供快照、预算和可观测性共用。
func Cost(weight uint32) Option { return func(my *Define) { my.Cost = weight } }

// Access binds a custom Resolver to the governed resource and actions it
// performs. Engines without a PolicyProvider may omit it for compatibility.
func Access(resource string, actions ...string) Option {
	return func(my *Define) {
		my.Resource = resource
		my.Actions = append([]string(nil), actions...)
	}
}

// ConsumePolicy declares that custom handler code reads and enforces
// PolicyDecisionFromContext when a provider returns row/preset/check facts.
func ConsumePolicy() Option { return func(my *Define) { my.PolicyAware = true } }

// Requires 声明实体字段Resolver依赖的宿主字段；planner会内部补投影并在响应前剥除。
func Requires(fields ...string) Option {
	return func(my *Define) { my.Requires = append([]string(nil), fields...) }
}

// Timeout 声明Handler的协作式超时；实现返回后引擎仍会检查deadline。
func Timeout(duration time.Duration) Option { return func(my *Define) { my.Timeout = duration } }

// RetrySafe 显式声明Handler可按稳定幂等键安全重试；默认永远为false。
func RetrySafe(idempotencyKey string) Option {
	return func(my *Define) { my.RetrySafe, my.IdempotencyKey = true, idempotencyKey }
}

// MaxBatch 限制一次Batch Resolver接收的最大宿主数；0表示使用引擎默认。
func MaxBatch(size int) Option { return func(my *Define) { my.MaxBatch = size } }

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

// Root 是Query/Mutation根字段的零大小强类型source。
type Root struct{}

// NewResolver 为任意GraphQL字段创建同一种泛型Resolver。parent可为
// Query、Mutation或实体类型；类型擦除仅发生在引擎注册边界，业务函数全程强类型。
func NewResolver[S, I, O any](parent, name, doc string,
	fn func(context.Context, S, I) (O, error), opts ...Option,
) Resolver {
	define := defineField[I, O](parent, name, doc)
	for _, opt := range opts {
		opt(&define)
	}
	return &fieldResolver[S, I, O]{define: define, fn: fn}
}

type fieldResolver[S, I, O any] struct {
	define Define
	fn     func(context.Context, S, I) (O, error)
}

func (my *fieldResolver[S, I, O]) Name() string   { return my.define.Class + "." + my.define.Name }
func (my *fieldResolver[S, I, O]) Define() Define { return my.define }

func (my *fieldResolver[S, I, O]) Resolve(ctx context.Context, source any, args map[string]interface{}) (interface{}, error) {
	typed, ok := source.(S)
	if !ok {
		return nil, fmt.Errorf("resolver %s source类型不匹配: %T", my.Name(), source)
	}
	in, err := decode[I](args)
	if err != nil {
		return nil, err
	}
	out, err := my.resolveTyped(ctx, typed, in)
	if err != nil {
		return nil, err
	}
	return erase(out), nil
}

func (my *fieldResolver[S, I, O]) resolveTyped(ctx context.Context, source S, input I) (O, error) {
	return my.fn(ctx, source, input)
}

// NewBatch 批量版NewResolver：整结果集一次调用免N+1，返回值须与sources等长对位
func NewBatch[I, O any](class, name, doc string, fn func(context.Context, []Source, I) ([]O, error), opts ...Option) Resolver {
	define := defineField[I, O](class, name, doc)
	for _, opt := range opts {
		opt(&define)
	}
	return &batch[I, O]{define: define, fn: fn}
}

// defineField 字段级声明：Class非空使sdl()渲染为 extend type <Class>
func defineField[I, O any](class, name, doc string) Define {
	input, output := reflect.TypeFor[I](), reflect.TypeFor[O]()
	return Define{
		Class: class, Name: name, Doc: doc,
		Args: deriveArgs(input), Result: deriveType(output, false),
		inputType: input, outputType: output,
	}
}

// batch 批量解析器（NewBatch反射装配）
type batch[I, O any] struct {
	define Define
	fn     func(context.Context, []Source, I) ([]O, error)
}

func (my *batch[I, O]) Name() string   { return my.define.Class + "." + my.define.Name }
func (my *batch[I, O]) Define() Define { return my.define }

func (my *batch[I, O]) Resolve(context.Context, any, map[string]interface{}) (interface{}, error) {
	return nil, fmt.Errorf("批量解析器不支持单对象路径")
}

func (my *batch[I, O]) ResolveBatch(ctx context.Context, sources []map[string]interface{}, args map[string]interface{}) ([]interface{}, error) {
	in, err := decode[I](args)
	if err != nil {
		return nil, err
	}
	outs, err := my.resolveTyped(ctx, sources, in)
	if err != nil {
		return nil, err
	}
	values := make([]interface{}, len(outs))
	for i, out := range outs {
		values[i] = erase(out)
	}
	return values, nil
}

func (my *batch[I, O]) resolveTyped(ctx context.Context, sources []Source, input I) ([]O, error) {
	return my.fn(ctx, sources, input)
}

// NewLoader creates a batch Resolver that loads values by a raw database key.
// Duplicate keys are fetched once; missing keys become null. The loader may
// call a local database, REST, gRPC, or another GraphQL service.
func NewLoader[O any](class, name, doc, key string, fn func(context.Context, []any) (map[any]O, error), opts ...Option) Resolver {
	define := defineField[struct{}, O](class, name, doc)
	for _, opt := range opts {
		opt(&define)
	}
	define.batchKey = key
	resolver := &batch[struct{}, any]{define: define}
	resolver.fn = func(ctx context.Context, sources []Source, _ struct{}) ([]any, error) {
		seen := make(map[any]bool, len(sources))
		keys := make([]any, 0, len(sources))
		alias := "__rk_" + key
		for _, source := range sources {
			value := source[alias]
			if value == nil {
				continue
			}
			if !reflect.TypeOf(value).Comparable() {
				return nil, fmt.Errorf("loader %s key is not comparable: %T", resolver.Name(), value)
			}
			if !seen[value] {
				seen[value] = true
				keys = append(keys, value)
			}
		}
		if len(keys) > handlerBatchLimit(resolver) {
			return nil, fmt.Errorf("batch超过限制: %d", handlerBatchLimit(resolver))
		}
		out := make([]any, len(sources))
		if len(keys) == 0 {
			return out, nil
		}
		values, err := fn(ctx, keys)
		if err != nil {
			return nil, err
		}
		for i, source := range sources {
			if value, ok := values[source[alias]]; ok {
				out[i] = plain(value)
			}
		}
		return out, nil
	}
	return resolver
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

// deref 剥离指针层取底层类型
func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t
}

// eachField 遍历导出字段：json标签定名（"-"跳过，缺省小驼峰），doc标签作描述
func eachField(t reflect.Type, fn func(f reflect.StructField, name, doc string)) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if !f.IsExported() || name == "-" {
			continue
		}
		if name == "" {
			name = strcase.ToLowerCamel(f.Name)
		}
		fn(f, name, f.Tag.Get("doc"))
	}
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
	t = deref(t)
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("Resolver入参必须是struct（无参用struct{}），收到 %s", t))
	}
	var parts []string
	eachField(t, func(f reflect.StructField, name, doc string) {
		kind := deriveType(f.Type, true)
		if required(f.Tag.Get("validate")) {
			kind += "!"
		}
		part := name + ": " + kind
		if doc != "" {
			part = `"` + doc + `" ` + part
		}
		parts = append(parts, part)
	})
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

func deriveType(t reflect.Type, input bool) string {
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
		return "[" + deriveType(t.Elem(), input) + bang + "]"
	case reflect.Struct:
		if t.Name() != "" {
			if input {
				return t.Name() + "Input"
			}
			return t.Name()
		}
		return protocol.SCALAR_JSON
	default: // map/interface等开放形态按Json透传
		return protocol.SCALAR_JSON
	}
}

// generatedTypes emits named shapes reachable from the resolver signature.
// Top-level inputs become arguments; existing schema types are reused.
func generatedTypes(d Define, known map[string]bool) string {
	var definitions []string
	seen := map[string]bool{}
	var collect func(reflect.Type, bool)
	collect = func(t reflect.Type, input bool) {
		for t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			t = t.Elem()
		}
		if _, scalar := scalars[t]; scalar || t.Kind() != reflect.Struct || t.Name() == "" {
			return
		}
		name := t.Name()
		kind := "type"
		if input {
			name, kind = name+"Input", "input"
		}
		if seen[name] {
			return
		}
		seen[name] = true // cycles are legal only when nullable; parser validates the final SDL
		eachField(t, func(field reflect.StructField, _, _ string) { collect(field.Type, input) })
		if known[name] {
			return
		}
		known[name] = true
		var sb strings.Builder
		fmt.Fprintf(&sb, "%s %s {\n", kind, name)
		eachField(t, func(field reflect.StructField, fieldName, doc string) {
			if doc != "" {
				fmt.Fprintf(&sb, "  \"\"\"%s\"\"\"\n", doc)
			}
			fieldType := deriveType(field.Type, input)
			if input && required(field.Tag.Get("validate")) {
				fieldType += "!"
			}
			fmt.Fprintf(&sb, "  %s: %s\n", fieldName, fieldType)
		})
		sb.WriteString("}")
		definitions = append(definitions, sb.String())
	}

	if d.inputType != nil {
		input := deref(d.inputType)
		if input.Kind() == reflect.Struct {
			eachField(input, func(field reflect.StructField, _, _ string) { collect(field.Type, true) })
		}
	}
	// Explicit Extra remains the escape hatch and owns output declarations.
	if d.Extra == "" && d.outputType != nil && d.Result == deriveType(d.outputType, false) {
		collect(d.outputType, false)
	}
	return strings.Join(definitions, "\n")
}

// scalars 特化标量映射（先于Kind分派）
var scalars = map[reflect.Type]string{
	reflect.TypeFor[std.Id]():    "ID",
	reflect.TypeFor[time.Time](): protocol.SCALAR_DATE_TIME,
}
