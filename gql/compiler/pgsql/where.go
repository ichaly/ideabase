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
	conditions, err := my.collectConditions(args)
	if err != nil {
		return err
	}
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

// collectConditions 收集可渲染的WHERE条件（id参数转换为主键等值条件；空对象剪枝）
func (my *Dialect) collectConditions(args ast.ArgumentList) ([]*ast.Value, error) {
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
		if whereArg.Value.Kind == ast.Variable {
			return nil, fmt.Errorf("where暂不支持整体变量，请内联条件或使用字段级变量")
		}
		if renderable(whereArg.Value) {
			conditions = append(conditions, whereArg.Value)
		}
	}

	return conditions, nil
}

// renderable 条件值是否会产出SQL片段；空对象语义为无条件恒真，整体剪枝
func renderable(value *ast.Value) bool {
	if value == nil {
		return false
	}
	for _, child := range value.Children {
		if renderableChild(child) {
			return true
		}
	}
	return false
}

// renderableChild 子条件是否会产出SQL片段（逻辑操作符递归剪枝，字段条件恒渲染）
func renderableChild(child *ast.ChildValue) bool {
	switch child.Name {
	case protocol.AND, protocol.OR:
		if child.Value == nil {
			return false
		}
		for _, sub := range child.Value.Children {
			if renderable(sub.Value) {
				return true
			}
		}
		return false
	case protocol.NOT:
		return renderable(child.Value)
	default:
		return true
	}
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

// buildCondition 构建单个条件值（对象条件的子项以AND连接；不可渲染的子项剪枝）
func (my *Dialect) buildCondition(ctx *compiler.Context, sc scope, value *ast.Value) error {
	if value == nil {
		return nil
	}
	children := make([]*ast.ChildValue, 0, len(value.Children))
	for _, child := range value.Children {
		if renderableChild(child) {
			children = append(children, child)
		}
	}

	if len(children) > 1 {
		ctx.Write("(")
	}
	for i, child := range children {
		if i > 0 {
			ctx.Space("AND")
		}
		if err := my.buildChild(ctx, sc, child); err != nil {
			return err
		}
	}
	if len(children) > 1 {
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
		values := make([]*ast.Value, 0, len(child.Value.Children))
		for _, sub := range child.Value.Children { // 空对象元素剪枝，剩余项才参与连接
			if renderable(sub.Value) {
				values = append(values, sub.Value)
			}
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

	// 字面量空列表恒不匹配：编译为FALSE（IN ()非法SQL，与变量路径= ANY('{}')语义对齐）
	if opChild.Name == protocol.IN && value.Kind == ast.ListValue && len(value.Children) == 0 {
		ctx.Write("FALSE")
		return nil
	}

	// IN变量走数组单槽位绑定（= ANY($n)）：SQL不依赖元素个数，计划保持可缓存；
	// 空数组恒不匹配，单值由列表槽位在执行期按规范强转
	if opChild.Name == protocol.IN && value.Kind == ast.Variable {
		lhs()
		ctx.Write(` = ANY(`, my.Placeholder(ctx.AddListVariable(value.Raw)), `)`)
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

// scopeCondition 写一条行级作用域过滤："限定符"."列" = $ctx（值执行期从请求上下文取）
func (my *Dialect) scopeCondition(ctx *compiler.Context, qualifier string, rule protocol.ScopeRule) {
	ctx.Column(qualifier, rule.Column).Write(` = `)
	ctx.Write(my.Placeholder(ctx.AddContextSlot(rule.Context)))
}

// scopeConjuncts 实体行级作用域的合取条件（每条 列=上下文值），查询/变更WHERE注入共用
func (my *Dialect) scopeConjuncts(ctx *compiler.Context, qualifier string, class *protocol.Class) []func() error {
	conjuncts := make([]func() error, len(class.Scope))
	for i, rule := range class.Scope {
		conjuncts[i] = func() error {
			my.scopeCondition(ctx, qualifier, rule)
			return nil
		}
	}
	return conjuncts
}

// appendScope 在已有WHERE后追加 AND 作用域条件（递归CTE层、关系操作目标行校验）
func (my *Dialect) appendScope(ctx *compiler.Context, table string, rules []protocol.ScopeRule) {
	for _, rule := range rules {
		ctx.Space(`AND`)
		my.scopeCondition(ctx, table, rule)
	}
}
