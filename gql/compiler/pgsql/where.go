// Package pgsql WHERE子句处理模块
package pgsql

import (
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2/ast"
)

// scope 描述当前条件所处的实体与SQL限定符（表名或别名）
type scope struct {
	class     *protocol.Class
	qualifier string
}

// column 将GraphQL字段名映射为列名，未知字段原样返回
func (my scope) column(fieldName string) string {
	if my.class != nil {
		if field, ok := my.class.Fields[fieldName]; ok && field.Column != "" {
			return field.Column
		}
	}
	return fieldName
}

// buildWhere 构建WHERE子句；conjuncts为前置合取条件（关联/搜索/keyset），与用户条件AND组合
func (my *Dialect) buildWhere(ctx *compiler.Context, sc scope, args ast.ArgumentList, conjuncts ...func() error) error {
	conditions := my.collectConditions(args)
	if len(conjuncts) == 0 && len(conditions) == 0 {
		return nil
	}

	ctx.Space("WHERE")
	for i, conjunct := range conjuncts {
		if i > 0 {
			ctx.Space("AND")
		}
		if err := conjunct(); err != nil {
			return err
		}
	}
	if len(conditions) == 0 {
		return nil
	}
	if len(conjuncts) > 0 {
		ctx.Space("AND")
	}
	return my.buildConditionList(ctx, sc, conditions, "AND", len(conditions) > 1)
}

// collectConditions 收集所有WHERE条件（id参数转换为主键等值条件）
func (my *Dialect) collectConditions(args ast.ArgumentList) []*ast.Value {
	var conditions []*ast.Value

	if idArg := args.ForName(protocol.ID); idArg != nil && idArg.Value != nil {
		conditions = append(conditions, &ast.Value{
			Kind: ast.ObjectValue,
			Children: []*ast.ChildValue{{
				Name: protocol.ID,
				Value: &ast.Value{
					Kind:     ast.ObjectValue,
					Children: []*ast.ChildValue{{Name: protocol.EQ, Value: idArg.Value}},
				},
			}},
		})
	}

	if whereArg := args.ForName(protocol.WHERE); whereArg != nil && whereArg.Value != nil {
		conditions = append(conditions, whereArg.Value)
	}

	return conditions
}

// buildConditionList 用指定逻辑操作符连接条件列表，wrap控制是否加括号
func (my *Dialect) buildConditionList(ctx *compiler.Context, sc scope, conditions []*ast.Value, operator string, wrap bool) error {
	if wrap {
		ctx.Write("(")
	}
	for i, condition := range conditions {
		if i > 0 {
			ctx.Space(operator)
		}
		if err := my.buildCondition(ctx, sc, condition); err != nil {
			return err
		}
	}
	if wrap {
		ctx.Write(")")
	}
	return nil
}

// buildCondition 构建单个条件值（对象条件的子项以AND连接）
func (my *Dialect) buildCondition(ctx *compiler.Context, sc scope, value *ast.Value) error {
	if value == nil || len(value.Children) == 0 {
		return nil
	}

	if len(value.Children) > 1 {
		ctx.Write("(")
	}
	for i, child := range value.Children {
		if i > 0 {
			ctx.Space("AND")
		}
		if err := my.buildChild(ctx, sc, child); err != nil {
			return err
		}
	}
	if len(value.Children) > 1 {
		ctx.Write(")")
	}
	return nil
}

// buildChild 分发子条件：逻辑操作符或字段条件
func (my *Dialect) buildChild(ctx *compiler.Context, sc scope, child *ast.ChildValue) error {
	if child == nil || child.Name == "" {
		return fmt.Errorf("无效的条件：名称为空")
	}

	switch child.Name {
	case protocol.AND, protocol.OR:
		if child.Value == nil || len(child.Value.Children) == 0 {
			return fmt.Errorf("逻辑操作符 %s 至少需要一个条件", child.Name)
		}
		values := make([]*ast.Value, len(child.Value.Children))
		for i, sub := range child.Value.Children {
			values[i] = sub.Value
		}
		return my.buildConditionList(ctx, sc, values, strings.ToUpper(child.Name), true)
	case protocol.NOT:
		if child.Value == nil {
			return fmt.Errorf("NOT操作符需要一个条件")
		}
		ctx.Write("NOT (")
		err := my.buildCondition(ctx, sc, child.Value)
		ctx.Write(")")
		return err
	default:
		return my.buildFieldCondition(ctx, sc, child)
	}
}

// buildFieldCondition 构建字段条件：字段引用 + 操作符 + 值
func (my *Dialect) buildFieldCondition(ctx *compiler.Context, sc scope, child *ast.ChildValue) error {
	if child.Value == nil || len(child.Value.Children) == 0 {
		return fmt.Errorf("字段条件 %s 缺少操作符和值", child.Name)
	}

	column := sc.column(child.Name)
	lhs := func() { ctx.Column(sc.qualifier, column) } // 左值为列引用
	for i, opChild := range child.Value.Children {
		if i > 0 {
			ctx.Space("AND")
		}
		if err := my.buildOperator(ctx, lhs, opChild); err != nil {
			return err
		}
	}
	return nil
}

// jsonbFunctions 以函数形式渲染的操作符（函数名即protocol.Operator.Value）
var jsonbFunctions = map[string]bool{
	protocol.CONTAINS:     true,
	protocol.CONTAINED_IN: true,
	protocol.HAS_KEY:      true,
}

// buildOperator 构建单个条件表达式：lhs写左值（列引用或聚合表达式），op + 参数。
// 左值抽象使where（列）与having（聚合）共享同一操作符引擎
func (my *Dialect) buildOperator(ctx *compiler.Context, lhs func(), opChild *ast.ChildValue) error {
	op, ok := protocol.GetOperator(opChild.Name)
	if !ok {
		return fmt.Errorf("不支持的操作符: %s", opChild.Name)
	}
	value := opChild.Value
	if value == nil {
		return fmt.Errorf("操作符 %s 缺少值", opChild.Name)
	}

	// jsonb函数式操作符：jsonb_contains(列, $n::jsonb) / jsonb_exists(列, $n)
	// （gorm会劫持@>/<@/?符号形态，函数形式语义与索引利用一致）
	if jsonbFunctions[opChild.Name] {
		ctx.Write(op.Value, `(`)
		lhs()
		ctx.Write(`, `)
		if err := my.buildParam(ctx, value); err != nil {
			return err
		}
		if opChild.Name != protocol.HAS_KEY {
			ctx.Write(`::jsonb`)
		}
		ctx.Write(`)`)
		return nil
	}

	lhs()
	ctx.Space(op.Value)

	switch opChild.Name {
	case protocol.IN:
		ctx.Write("(")
		if value.Kind == ast.ListValue {
			for i, child := range value.Children {
				if i > 0 {
					ctx.Write(", ")
				}
				if err := my.buildParam(ctx, child.Value); err != nil {
					return err
				}
			}
		} else if err := my.buildParam(ctx, value); err != nil {
			return err
		}
		ctx.Write(")")
		return nil
	case protocol.IS:
		// IsInput枚举：NULL / NOT_NULL
		switch value.Raw {
		case "NULL":
			ctx.Write("NULL")
		case "NOT_NULL":
			ctx.Write("NOT NULL")
		default:
			return fmt.Errorf("IS操作符需要NULL或NOT_NULL，得到 %s", value.Raw)
		}
		return nil
	default:
		return my.buildParam(ctx, value)
	}
}

// buildParam 构建参数：变量记录为槽位引用，字面量直接取值
func (my *Dialect) buildParam(ctx *compiler.Context, value *ast.Value) error {
	if value == nil {
		return fmt.Errorf("参数值为空")
	}

	if value.Kind == ast.Variable {
		ctx.Write(my.Placeholder(ctx.AddVariable(value.Raw)))
		return nil
	}

	val, err := value.Value(nil)
	if err != nil {
		return fmt.Errorf("获取参数值失败: %w", err)
	}
	ctx.Write(my.Placeholder(ctx.AddParam(normalizeArg(val))))
	return nil
}
