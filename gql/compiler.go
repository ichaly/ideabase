package gql

import (
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/vektah/gqlparser/v2/ast"
)

// Compiler 编译上下文
type Compiler struct {
	meta    *Metadata        // 元数据引用
	dialect compiler.Dialect // 方言实现引用，避免重复查询
}

// NewCompiler 创建新的编译上下文
// dialects传nil时使用自注册的方言（方言包init注册，空白导入即启用）
func NewCompiler(m *Metadata, dialects []compiler.Dialect) (*Compiler, error) {
	if len(dialects) == 0 {
		dialects = compiler.Dialects()
	}
	my := &Compiler{meta: m}
	if err := my.selectDialect(dialects); err != nil {
		return nil, err
	}
	return my, nil
}

// Plan 编译产物：SQL + 参数槽位 + 变量默认值 + resolver绑定
// 非volatile的计划可按查询文本缓存，执行期仅需解析参数槽位
type Plan struct {
	SQL       string
	slots     []compiler.Slot
	defaults  map[string]interface{}
	volatile  bool
	resolvers []binding
	tables    []string   // 涉及的表集合，订阅按表变更唤醒
	paths     codecPaths // 选择集中codec字段路径树，出参流式转换用（executor首次编译时固化）
}

// Volatile 编译产物是否依赖变量内容（如整体input变量），不可缓存
func (my *Plan) Volatile() bool {
	return my.volatile
}

// ResolveArgs 按变量表解析参数槽位，变量缺失（而非显式null）时回退操作定义的默认值；
// 默认值并入变量表统一解析，游标/列表槽位同样经解码与规范化；
// scope 提供行级作用域值（租户/属主，认证注入），无作用域时传nil
func (my *Plan) ResolveArgs(variables, scope map[string]interface{}) ([]any, error) {
	if len(my.defaults) > 0 {
		merged := make(map[string]interface{}, len(variables)+len(my.defaults))
		for k, v := range my.defaults {
			merged[k] = v
		}
		for k, v := range variables { // 显式传入（含null）覆盖默认值，符合GraphQL规范
			merged[k] = v
		}
		variables = merged
	}
	return compiler.ResolveSlots(my.slots, variables, scope)
}

// depthOf 选择集最大嵌套深度，叶子字段计1层（fragment已在parse期展开为纯字段）
func depthOf(set ast.SelectionSet) int {
	deepest := 0
	for _, s := range set {
		if f, ok := s.(*ast.Field); ok {
			if d := depthOf(f.SelectionSet) + 1; d > deepest {
				deepest = d
			}
		}
	}
	return deepest
}

// inline 展开选择集中的fragment（命名与内联），编译器只需处理纯字段
// fragment重复引用时展开是幂等的（展开后不再有spread）
func inline(set ast.SelectionSet, fragments ast.FragmentDefinitionList) ast.SelectionSet {
	out := make(ast.SelectionSet, 0, len(set))
	for _, selection := range set {
		switch s := selection.(type) {
		case *ast.Field:
			s.SelectionSet = inline(s.SelectionSet, fragments)
			out = append(out, s)
		case *ast.FragmentSpread:
			if fragment := fragments.ForName(s.Name); fragment != nil {
				out = append(out, inline(fragment.SelectionSet, fragments)...)
			}
		case *ast.InlineFragment:
			out = append(out, inline(s.SelectionSet, fragments)...)
		}
	}
	return out
}

// Compile 编译GraphQL操作为执行计划
func (my *Compiler) Compile(operation *ast.OperationDefinition, variables map[string]interface{}) (*Plan, error) {
	// 深度护栏：嵌套LATERAL单元无上限时深选择集可编译出巨大SQL树（代价攻击面）
	if max := my.meta.MaxDepth(); max > 0 {
		if depth := depthOf(operation.SelectionSet); depth > max {
			return nil, fmt.Errorf("查询嵌套深度%d超过上限%d（schema.max-depth可调，0=不限制）", depth, max)
		}
	}
	ctx := compiler.NewContext(my.meta, my.dialect.Quotation(), variables)
	defer ctx.Release()

	var err error
	switch operation.Operation {
	case ast.Query, ast.Subscription:
		err = my.dialect.BuildQuery(ctx, operation.SelectionSet)
	case ast.Mutation:
		err = my.dialect.BuildMutation(ctx, operation.SelectionSet)
	}
	if err != nil {
		return nil, err
	}

	plan := &Plan{
		SQL:      ctx.String(),
		slots:    ctx.Slots(),
		volatile: ctx.Volatile(),
		tables:   ctx.Tables(),
	}
	for _, def := range operation.VariableDefinitions {
		if def.DefaultValue == nil {
			continue
		}
		if value, err := def.DefaultValue.Value(nil); err == nil {
			if plan.defaults == nil {
				plan.defaults = make(map[string]interface{})
			}
			plan.defaults[def.Variable] = value
		}
	}
	return plan, nil
}

// Build 编译并立即解析参数（一次性场景与测试）
func (my *Compiler) Build(operation *ast.OperationDefinition, variables map[string]interface{}) (string, []any, error) {
	plan, err := my.Compile(operation, variables)
	if err != nil {
		return "", nil, err
	}
	args, err := plan.ResolveArgs(variables, nil)
	if err != nil {
		return "", nil, err
	}
	return plan.SQL, args, nil
}

// selectDialect 选择适合当前数据库的SQL方言
// 已知驱动严格匹配同名方言，未注册时明确报错（静默回退会生成错误SQL更难排查）；
// 无数据库连接（纯编译场景）时优先PostgreSQL方言，否则取首个可用方言
func (my *Compiler) selectDialect(list []compiler.Dialect) error {
	dialects := make(map[string]compiler.Dialect, len(list))
	for _, dialect := range list {
		dialects[dialect.Name()] = dialect
	}

	// 已知驱动严格按名匹配
	if my.meta != nil && my.meta.db != nil {
		driver := my.meta.db.Name()
		for prefix, name := range map[string]string{"postgres": "postgresql", "mysql": "mysql"} {
			if !strings.Contains(driver, prefix) {
				continue
			}
			dialect, ok := dialects[name]
			if !ok {
				return fmt.Errorf("数据库驱动 %s 没有注册对应的SQL方言实现", driver)
			}
			my.dialect = dialect
			return nil
		}
	}

	// 无连接或未知驱动：优先PostgreSQL，否则首个可用
	if dialect, ok := dialects["postgresql"]; ok {
		my.dialect = dialect
		return nil
	}
	for _, dialect := range dialects {
		my.dialect = dialect
		return nil
	}
	return fmt.Errorf("没有可用的SQL方言实现")
}
