package compiler

import (
	"github.com/vektah/gqlparser/v2/ast"
)

// Dialect SQL方言接口（策略模式扩展点）
//
// 新增数据库支持只需实现本接口并在组装时注册，如：
// NewCompiler(meta, []compiler.Dialect{pgsql.NewDialect(), mysql.NewDialect()})
// 方言按数据库驱动名自动路由，编译器/执行器/缓存无需任何改动。
//
// 实现必须遵守的契约：
//  1. BuildQuery/BuildMutation 产出的SQL必须返回【单行单列】结果，
//     列值为JSON对象（pgsql用JSONB_BUILD_OBJECT，MySQL可用JSON_OBJECT），
//     顶层key为GraphQL字段别名——执行器按此契约解包为data
//  2. 根查询字段遵循 XxxResult 契约（items数组 + 可选total），
//     嵌套列表关系输出纯数组，单对象关系输出对象或null
//  3. 参数一律经 ctx.AddParam（字面量）/ ctx.AddVariable（变量引用）
//     登记为槽位并配合 Placeholder 写占位符——编译产物据此按查询文本缓存
//  4. 编译涉及的每张表调用 ctx.MarkTable 登记——订阅(CDC)据此按表变更唤醒
//  5. 子查询别名使用 ctx.NextIndex 全局自增序号，避免递归冲突
//  6. 仅依赖 protocol 包读取元数据，不得import gql主包（会形成import cycle）
type Dialect interface {
	// Name 方言名称，与数据库驱动名匹配（如 postgresql、mysql）
	Name() string

	// Quotation 标识符引号 (如: PostgreSQL的双引号, MySQL的反引号)
	Quotation() string

	// Placeholder 获取参数占位符 (如: PostgreSQL的$1,$2..., MySQL的?)
	Placeholder(index int) string

	// BuildQuery 构建查询语句
	BuildQuery(ctx *Context, set ast.SelectionSet) error

	// BuildMutation 构建变更语句
	BuildMutation(ctx *Context, set ast.SelectionSet) error
}
