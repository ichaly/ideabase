// Package pgsql WHERE子句处理模块
package pgsql

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
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
	conditions, err := my.collectConditions(ctx, args)
	if err != nil {
		return err
	}
	key, err := my.keyConjunct(ctx, sc, args.ForName(protocol.KEY))
	if err != nil {
		return err
	}
	if key != nil {
		conjuncts = append([]func() error{key}, conjuncts...)
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

// keyConjunct compiles a typed composite primary key. Whole-object variables
// are volatile because compiler slots address top-level variables, not nested
// members; recompiling preserves correct values without adding a second slot model.
func (my *Dialect) keyConjunct(ctx *compiler.Context, sc scope, arg *ast.Argument) (func() error, error) {
	if arg == nil || arg.Value == nil {
		return nil, nil
	}
	if arg.Value.Kind == ast.NullValue {
		return nil, fmt.Errorf("key不能为null")
	}
	if sc.class == nil || len(sc.class.PrimaryKeys) < 2 {
		return nil, fmt.Errorf("key仅适用于复合主键实体")
	}
	fields := make([]*protocol.Field, len(sc.class.PrimaryKeys))
	for i, name := range sc.class.PrimaryKeys {
		field := sc.class.Fields[name]
		if field == nil || field.Column == "" {
			return nil, fmt.Errorf("复合主键字段不存在: %s.%s", sc.class.Name, name)
		}
		fields[i] = field
	}

	if arg.Value.Kind == ast.Variable {
		raw, ok := ctx.Variable(arg.Value.Raw)
		values, valid := raw.(map[string]interface{})
		if !ok || !valid {
			return nil, fmt.Errorf("key变量 %s 缺失或不是对象", arg.Value.Raw)
		}
		params := make([]interface{}, len(fields))
		for i, field := range fields {
			value, exists := values[field.Name]
			if !exists || value == nil {
				return nil, fmt.Errorf("key变量缺少字段: %s", field.Name)
			}
			params[i] = normalizeArg(value)
		}
		ctx.MarkVolatile()
		return func() error {
			return writeKeyEquals(ctx, sc, fields, func(i int) error {
				ctx.Write(my.Placeholder(ctx.AddParam(params[i])))
				return nil
			})
		}, nil
	}

	values := make(map[string]*ast.Value, len(arg.Value.Children))
	for _, child := range arg.Value.Children {
		if child != nil {
			values[child.Name] = child.Value
		}
	}
	for _, field := range fields {
		if values[field.Name] == nil {
			return nil, fmt.Errorf("key缺少字段: %s", field.Name)
		}
	}
	return func() error {
		return writeKeyEquals(ctx, sc, fields, func(i int) error { return my.buildParam(ctx, values[fields[i].Name]) })
	}, nil
}

func writeKeyEquals(ctx *compiler.Context, sc scope, fields []*protocol.Field, value func(int) error) error {
	ctx.Write("(")
	for i, field := range fields {
		if i > 0 {
			ctx.Space("AND")
		}
		ctx.Column(sc.qualifier, field.Column).Write(" = ")
		if err := value(i); err != nil {
			return err
		}
	}
	ctx.Write(")")
	return nil
}

// collectConditions 收集可渲染的WHERE条件（id参数转换为主键等值条件；空对象剪枝）
func (my *Dialect) collectConditions(ctx *compiler.Context, args ast.ArgumentList) ([]*ast.Value, error) {
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
			raw, ok := ctx.Variable(whereArg.Value.Raw)
			if !ok || raw == nil {
				return nil, fmt.Errorf("where变量 %s 缺失或为null", whereArg.Value.Raw)
			}
			value, ok := runtimeWhereValue(raw)
			if !ok || value.Kind != ast.ObjectValue {
				return nil, fmt.Errorf("where变量 %s 必须是对象", whereArg.Value.Raw)
			}
			// Object shape changes the emitted SQL, so this plan cannot be
			// reused solely by query text.
			ctx.MarkVolatile()
			if renderable(value) {
				conditions = append(conditions, value)
			}
			return conditions, nil
		}
		if renderable(whereArg.Value) {
			conditions = append(conditions, whereArg.Value)
		}
	}

	return conditions, nil
}

// runtimeWhereValue converts an already GraphQL-coerced input object back to
// the small AST shape consumed by the single condition compiler. Keeping one
// renderer means literal and whole-variable where inputs have identical SQL
// semantics, including nested and/or/not operators.
func runtimeWhereValue(raw any) (*ast.Value, bool) {
	if raw == nil {
		return &ast.Value{Kind: ast.NullValue, Raw: "null"}, true
	}
	rv := reflect.ValueOf(raw)
	for rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return &ast.Value{Kind: ast.NullValue, Raw: "null"}, true
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return nil, false
		}
		keys := rv.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		value := &ast.Value{Kind: ast.ObjectValue, Children: make(ast.ChildValueList, 0, len(keys))}
		for _, key := range keys {
			child, ok := runtimeWhereValue(rv.MapIndex(key).Interface())
			if !ok {
				return nil, false
			}
			value.Children = append(value.Children, &ast.ChildValue{Name: key.String(), Value: child})
		}
		return value, true
	case reflect.Slice, reflect.Array:
		value := &ast.Value{Kind: ast.ListValue, Children: make(ast.ChildValueList, 0, rv.Len())}
		for i := 0; i < rv.Len(); i++ {
			child, ok := runtimeWhereValue(rv.Index(i).Interface())
			if !ok {
				return nil, false
			}
			value.Children = append(value.Children, &ast.ChildValue{Value: child})
		}
		return value, true
	case reflect.Bool:
		return &ast.Value{Kind: ast.BooleanValue, Raw: strconv.FormatBool(rv.Bool())}, true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return &ast.Value{Kind: ast.IntValue, Raw: strconv.FormatInt(rv.Int(), 10)}, true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &ast.Value{Kind: ast.IntValue, Raw: strconv.FormatUint(rv.Uint(), 10)}, true
	case reflect.Float32, reflect.Float64:
		return &ast.Value{Kind: ast.FloatValue, Raw: strconv.FormatFloat(rv.Float(), 'g', -1, rv.Type().Bits())}, true
	case reflect.String:
		return &ast.Value{Kind: ast.StringValue, Raw: rv.String()}, true
	default:
		return nil, false
	}
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

// predicateConjuncts renders metadata predicates as fixed SQL. Values are always
// slots so the compiled statement shape does not depend on request data.
func (my *Dialect) predicateConjuncts(ctx *compiler.Context, qualifier string, predicates []protocol.Predicate, skipColumn string) []func() error {
	out := make([]func() error, 0, len(predicates))
	for _, p := range predicates {
		p := p
		if p.Column == "" || (p.Column == skipColumn && p.Operator == "is-null") {
			continue
		}
		out = append(out, func() error {
			return my.writePredicate(ctx, qualifier, p)
		})
	}
	return out
}

func (my *Dialect) writePredicate(ctx *compiler.Context, qualifier string, p protocol.Predicate) error {
	ctx.Column(qualifier, p.Column)
	switch p.Operator {
	case "eq", "ne", "lt", "lte", "gt", "gte":
		operator := "="
		switch p.Operator {
		case "ne":
			operator = "<>"
		case "lt":
			operator = "<"
		case "lte":
			operator = "<="
		case "gt":
			operator = ">"
		case "gte":
			operator = ">="
		}
		ctx.Space(operator)
		if p.Context != "" {
			ctx.Write(my.Placeholder(ctx.AddContextSlot(p.Context)))
		} else {
			ctx.Write(my.Placeholder(ctx.AddParam(normalizeArg(p.Value))))
		}
	case "in":
		ctx.Write(" = ANY(")
		if p.Context != "" {
			ctx.Write(my.Placeholder(ctx.AddContextListSlot(p.Context)))
		} else {
			ctx.Write(my.Placeholder(ctx.AddParam(compiler.ListArg(p.Value))))
		}
		ctx.Write(")")
	case "is-null":
		ctx.Write(" IS NULL")
	case "not-null", "is-not-null":
		ctx.Write(" IS NOT NULL")
	default:
		return fmt.Errorf("不支持的固定谓词操作符: %s", p.Operator)
	}
	return nil
}

// fixedWhereConjuncts combines metadata row predicates and role policy. It is
// deliberately role-independent: the Engine supplies policy roles at runtime.
func (my *Dialect) fixedWhereConjuncts(ctx *compiler.Context, qualifier string, class *protocol.Class, operation string, skipColumn string) []func() error {
	out := my.predicateConjuncts(ctx, qualifier, class.Where, skipColumn)
	if class.Permissions == nil {
		return out
	}
	roles := make([]string, 0, len(class.Permissions))
	for role, perm := range class.Permissions {
		allowed := false
		switch operation {
		case protocol.OP_SELECT:
			allowed = perm != nil && len(perm.Select) > 0
		case protocol.OP_AGGREGATE:
			allowed = perm != nil && perm.Aggregate
		case protocol.OP_UPDATE:
			allowed = perm != nil && len(perm.Update) > 0
		case protocol.OP_INSERT:
			allowed = perm != nil && len(perm.Insert) > 0
		case protocol.OP_DELETE:
			allowed = perm != nil && perm.Delete
		case protocol.OP_RESTORE:
			allowed = perm != nil && perm.Restore
		case protocol.OP_PURGE:
			allowed = perm != nil && perm.Purge
		}
		if allowed {
			roles = append(roles, role)
		}
	}
	sort.Strings(roles)
	out = append(out, func() error {
		if len(roles) == 0 {
			ctx.Write("FALSE")
			return nil
		}
		ctx.Write("(")
		for i, role := range roles {
			if i > 0 {
				ctx.Space("OR")
			}
			ctx.Write("(").Write(my.Placeholder(ctx.AddParam(role))).Write(" = ANY(").Write(my.Placeholder(ctx.AddContextListSlot(protocol.CONTEXT_ROLES))).Write(")")
			perm := class.Permissions[role]
			for _, p := range perm.Filter {
				ctx.Space("AND")
				if err := my.writePredicate(ctx, qualifier, p); err != nil {
					return err
				}
			}
			ctx.Write(")")
		}
		ctx.Write(")")
		return nil
	})
	return out
}

// queryWhereConjuncts applies the soft-delete view selected by the public
// deletion argument. Keeping the mode in one SQL slot preserves plan reuse for
// variable-driven requests while the default ACTIVE path stays minimal.
func (my *Dialect) queryWhereConjuncts(ctx *compiler.Context, qualifier string, class *protocol.Class, operation string, args ast.ArgumentList) []func() error {
	if class.SoftDelete == nil {
		return my.fixedWhereConjuncts(ctx, qualifier, class, operation, "")
	}
	arg := args.ForName(protocol.DELETION)
	if arg == nil || arg.Value == nil {
		return my.fixedWhereConjuncts(ctx, qualifier, class, operation, "")
	}
	column := class.SoftDelete.Column
	out := my.fixedWhereConjuncts(ctx, qualifier, class, operation, column)
	out = append(out, func() error {
		var slot int
		if arg.Value.Kind == ast.Variable {
			slot = ctx.AddVariable(arg.Value.Raw)
		} else {
			value, err := arg.Value.Value(nil)
			if err != nil {
				return fmt.Errorf("解析deletion参数失败: %w", err)
			}
			slot = ctx.AddParam(value)
		}
		mode := my.Placeholder(slot) + "::text"
		ctx.Write("(", mode, " = 'WITH_DELETED' OR (")
		ctx.Write(mode, " = 'ACTIVE' AND ").Column(qualifier, column).Write(" IS NULL) OR (")
		ctx.Write(mode, " = 'ONLY_DELETED' AND ").Column(qualifier, column).Write(" IS NOT NULL))")
		return nil
	})
	return out
}

// appendScope 在已有WHERE后追加 AND 作用域条件（递归CTE层、关系操作目标行校验）
func (my *Dialect) appendScope(ctx *compiler.Context, table string, rules []protocol.ScopeRule) {
	for _, rule := range rules {
		ctx.Space(`AND`)
		my.scopeCondition(ctx, table, rule)
	}
}
