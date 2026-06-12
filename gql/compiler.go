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
	tables    []string // 涉及的表集合，订阅按表变更唤醒
}

// Volatile 编译产物是否依赖变量内容（如整体input变量），不可缓存
func (my *Plan) Volatile() bool {
	return my.volatile
}

// Args 按变量表解析参数槽位，缺失变量回退到操作定义的默认值
func (my *Plan) Args(variables map[string]interface{}) []any {
	args := make([]any, len(my.slots))
	for i, slot := range my.slots {
		value := slot.Resolve(variables)
		if value == nil && slot.Variable != "" {
			value = my.defaults[slot.Variable]
		}
		args[i] = value
	}
	return args
}

// Compile 编译GraphQL操作为执行计划
func (my *Compiler) Compile(operation *ast.OperationDefinition, variables map[string]interface{}) (*Plan, error) {
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
		SQL:       ctx.String(),
		slots:     ctx.Slots(),
		volatile:  ctx.Volatile(),
		resolvers: collectBindings(my.meta, operation),
		tables:    ctx.Tables(),
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
	return plan.SQL, plan.Args(variables), nil
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
		for _, name := range []string{"postgres", "mysql"} {
			if !strings.Contains(driver, name) {
				continue
			}
			dialect, ok := dialects[strings.Replace(name, "postgres", "postgresql", 1)]
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
