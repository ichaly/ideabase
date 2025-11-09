package ioc

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/ichaly/ideabase/std"
	"go.uber.org/fx"
)

var (
	_ = Bind(newAdapter)
	_ = Bind(std.NewFiber)
	_ = Bind(std.NewHealth, As[std.Plugin](), Out("plugin"))
	_ = Invoke(std.Bootstrap, In("plugin", "filter"))
)

var options []fx.Option

type bootOption struct {
	opts []fx.Option
}

type BootOption func(*bootOption)

// Run 构建并启动容器，支持可选的启动参数。
func Run(opts ...BootOption) {
	cfg := &bootOption{}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	fxOpts := make([]fx.Option, 0, len(options)+len(cfg.opts)+1)
	if len(options) > 0 {
		fxOpts = append(fxOpts, fx.Options(options...))
	}
	if len(cfg.opts) > 0 {
		fxOpts = append(fxOpts, cfg.opts...)
	}
	fxOpts = append(fxOpts, fx.NopLogger)
	fx.New(fxOpts...).Run()
}

type bindOption struct {
	paramTags  []string
	resultTags []string
	extra      []Annotation
}

type BindOption func(*bindOption)

// Annotation 对 fx.Annotation 的别名，供业务调用避免直接依赖 fx。
type Annotation = fx.Annotation

// Bind 统一注册入口，减少重复书写 fx.Provide。
func Bind(ctor any, opts ...BindOption) struct{} {
	fnType := reflect.TypeOf(ctor)
	if fnType == nil || fnType.Kind() != reflect.Func {
		panic("ioc: Bind expects a constructor function")
	}

	anns := collectAnnotations(fnType, true, "Bind", opts...)
	options = append(options, fx.Provide(fx.Annotate(ctor, anns...)))
	return struct{}{}
}

// Invoke 统一触发入口，支持 In/With 等注解。
func Invoke(fn any, opts ...BindOption) struct{} {
	fnType := reflect.TypeOf(fn)
	if fnType == nil || fnType.Kind() != reflect.Func {
		panic("ioc: Invoke expects a function")
	}

	anns := collectAnnotations(fnType, false, "Invoke", opts...)
	options = append(options, fx.Invoke(fx.Annotate(fn, anns...)))
	return struct{}{}
}

// Entity 将类型注册到 entity 分组。
func Entity[T any](factory ...func() T) struct{} {
	var ctor func() any
	if len(factory) > 0 && factory[0] != nil {
		f := factory[0]
		ctor = func() any { return f() }
	} else {
		ctor = func() any {
			var v T
			return v
		}
	}
	options = append(options, fx.Provide(fx.Annotated{Group: "entity", Target: ctor}))
	return struct{}{}
}

// As 结果转换为接口类型。
func As[T any]() BindOption {
	return func(o *bindOption) {
		var zero T
		o.extra = append(o.extra, fx.As(&zero))
	}
}

// In 配置参数 Tags。
func In(tags ...string) BindOption {
	return func(o *bindOption) {
		for _, t := range tags {
			o.paramTags = append(o.paramTags, normalizeTag(t))
		}
	}
}

// Out 配置结果 Tags。
func Out(tags ...string) BindOption {
	return func(o *bindOption) {
		for _, t := range tags {
			o.resultTags = append(o.resultTags, normalizeTag(t))
		}
	}
}

// With 附加自定义 fx.Annotation，例如 fx.As / fx.Name。
func With(anns ...Annotation) BindOption {
	return func(o *bindOption) {
		o.extra = append(o.extra, anns...)
	}
}

// Supply 在容器启动时注入即时值。
func Supply(values ...any) BootOption {
	return func(o *bootOption) {
		if len(values) == 0 {
			return
		}
		o.opts = append(o.opts, fx.Supply(values...))
	}
}

func normalizeTag(tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return ""
	}
	if strings.Contains(tag, ":") {
		return tag
	}
	return `group:"` + tag + `"`
}

func collectAnnotations(fnType reflect.Type, allowResult bool, name string, opts ...BindOption) []Annotation {
	opt := &bindOption{}
	for _, o := range opts {
		if o != nil {
			o(opt)
		}
	}

	var anns []Annotation
	if len(opt.paramTags) > 0 {
		if len(opt.paramTags) > fnType.NumIn() {
			panic(fmt.Sprintf("ioc: %s In() tags count %d exceeds %d parameters", name, len(opt.paramTags), fnType.NumIn()))
		}
		// fx.ParamTags 需要与形参数量相同的切片，复制后可填补空缺并避免底层数组被后续 BindOption 修改。
		tags := make([]string, fnType.NumIn())
		copy(tags, opt.paramTags)
		anns = append(anns, fx.ParamTags(tags...))
	}
	if len(opt.resultTags) > 0 && allowResult {
		if len(opt.resultTags) != fnType.NumOut() {
			panic(fmt.Sprintf("ioc: %s Out() tags count %d mismatch %d results", name, len(opt.resultTags), fnType.NumOut()))
		}
		anns = append(anns, fx.ResultTags(opt.resultTags...))
	}
	if len(opt.extra) > 0 {
		anns = append(anns, opt.extra...)
	}
	return anns
}
