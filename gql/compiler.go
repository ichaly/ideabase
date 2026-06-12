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
func NewCompiler(m *Metadata, dialects []compiler.Dialect) (*Compiler, error) {
	my := &Compiler{meta: m}
	if err := my.selectDialect(dialects); err != nil {
		return nil, err
	}
	return my, nil
}

// Plan 编译产物：SQL + 参数槽位 + 变量默认值
// 非volatile的计划可按查询文本缓存，执行期仅需解析参数槽位
type Plan struct {
	SQL      string
	slots    []compiler.Slot
	defaults map[string]interface{}
	volatile bool
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

	plan := &Plan{SQL: ctx.String(), slots: ctx.Slots(), volatile: ctx.Volatile()}
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
// 方言选择逻辑:
// 1. 优先根据数据库驱动类型选择对应方言
// 2. 如未找到匹配，尝试使用PostgreSQL方言(推荐方言)
// 3. 如仍未找到，使用首个可用方言
// 4. 如无可用方言，返回错误
func (my *Compiler) selectDialect(list []compiler.Dialect) error {
	dialects := make(map[string]compiler.Dialect, len(list))
	for _, dialect := range list {
		dialects[dialect.Name()] = dialect
	}
	// 1. 首先尝试根据数据库类型选择方言
	if my.meta != nil && my.meta.db != nil {
		dbName := my.meta.db.Name()

		// 根据数据库驱动名称匹配方言
		switch {
		case strings.Contains(dbName, "postgres"):
			if dialect, ok := dialects["postgresql"]; ok {
				my.dialect = dialect
			}
		case strings.Contains(dbName, "mysql"):
			if dialect, ok := dialects["mysql"]; ok {
				my.dialect = dialect
			}
		}
	}

	// 2. 如果未找到匹配方言，尝试使用PostgreSQL方言（如果存在）
	if my.dialect == nil && len(dialects) > 0 {
		if dialect, ok := dialects["postgresql"]; ok {
			my.dialect = dialect
		} else {
			// 3. 否则使用第一个可用的方言
			for _, dialect := range dialects {
				my.dialect = dialect
				break
			}
		}
	}

	// 4. 如果仍未找到方言，返回错误
	if my.dialect == nil {
		return fmt.Errorf("没有可用的SQL方言实现")
	}

	return nil
}
