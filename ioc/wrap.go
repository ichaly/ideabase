package ioc

import "go.uber.org/fx"

// 对 fx 的轻量封装，便于未来替换 DI 框架时集中改造

// Option / Annotation / Annotated 类型别名，避免业务直接依赖 fx 包
type Option = fx.Option
type Annotated = fx.Annotated
type Annotation = fx.Annotation

// Options 聚合
func Options(opts ...Option) Option { return fx.Options(opts...) }

// Module 模块封装
func Module(name string, opts ...Option) Option { return fx.Module(name, opts...) }

// Provide 构造器注册
func Provide(constructors ...any) Option { return fx.Provide(constructors...) }

// Invoke 触发调用
func Invoke(funcs ...any) Option { return fx.Invoke(funcs...) }

// Annotate 注解封装（As/ResultTags/ParamTags 等）
func Annotate(target any, anns ...Annotation) any { return fx.Annotate(target, anns...) }

// As 结果转换为接口类型
func As(i any) Annotation { return fx.As(i) }

// ResultTags 标注结果 Tags
func ResultTags(tags ...string) Annotation { return fx.ResultTags(tags...) }

// ParamTags 标注参数 Tags
func ParamTags(tags ...string) Annotation { return fx.ParamTags(tags...) }

// Group 将目标加入指定分组（等价 fx.Annotated{Group:..., Target:...}）
func Group(name string, target any) any { return fx.Annotated{Group: name, Target: target} }
