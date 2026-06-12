// Package pgsql 实现PostgreSQL的SQL方言
// SELECT编译遵循 doc/pgsql-template-design.md：
// 每个实体字段编译为一个LATERAL JOIN子查询单元，关联条件由关系元数据驱动，
// 嵌套任意深度仍是单条SQL，从根上消除N+1
package pgsql

import (
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2/ast"
)

// unit 一个LATERAL JOIN子查询单元：查询根字段、关系字段或变更读回
type unit struct {
	field  *ast.Field         // GraphQL字段
	class  *protocol.Class    // 对应实体
	rel    *protocol.Relation // 与父级的关系，根字段为nil
	parent string             // 父级基表别名（lateral关联引用）
	index  int                // 单元序号，决定 __sj_N/__sr_N 别名
	single bool               // 单对象形态（多对一关系、变更读回）
	args   ast.ArgumentList   // 生效的查询参数；变更读回为nil（参数已被CTE消费）
}

// BuildQuery 构建查询语句：根JSON对象 + 每个根字段一个LATERAL单元
func (my *Dialect) BuildQuery(ctx *compiler.Context, set ast.SelectionSet) error {
	fields := fieldsOf(set)
	if len(fields) == 0 {
		return fmt.Errorf("查询选择集为空")
	}

	units := make([]*unit, 0, len(fields))
	ctx.Write(`SELECT JSONB_BUILD_OBJECT(`)
	for i, field := range fields {
		if i > 0 {
			ctx.Write(`, `)
		}
		if field.Name == typename {
			ctx.Write(`'`, field.Alias, `', 'Query'`)
			continue
		}

		className := strings.TrimSuffix(field.Definition.Type.Name(), protocol.SUFFIX_RESULT)
		class, ok := ctx.GetClass(className)
		if !ok {
			return fmt.Errorf("不支持的根查询字段: %s", field.Name)
		}
		u := &unit{field: field, class: class, index: ctx.NextIndex(), args: field.Arguments}
		units = append(units, u)
		ctx.Write(`'`, field.Alias, `', `).Quote(`__sj_`, u.index).Write(`."json"`)
	}
	ctx.Write(`) AS "__root" FROM (SELECT TRUE) AS "__root_x"`)

	for _, u := range units {
		if err := my.buildUnit(ctx, u); err != nil {
			return err
		}
	}
	return nil
}

// typename GraphQL元字段，编译为类型名字面量
const typename = "__typename"

// buildUnit 输出一个LATERAL JOIN单元，按单元形态选择JSON包装策略
func (my *Dialect) buildUnit(ctx *compiler.Context, u *unit) error {
	ctx.SpaceBefore(`LEFT OUTER JOIN LATERAL (`)

	var err error
	switch {
	case u.single: // 单对象：多对一关系或变更读回
		ctx.Write(`SELECT TO_JSONB(`).
			Quote(`__sr_`, u.index).Write(`.*) AS "json" FROM (`)
		err = my.buildCore(ctx, u, fieldsOf(u.field.SelectionSet), false)
		ctx.Write(`) AS `).Quote(`__sr_`, u.index)
	case u.rel == nil: // 查询根字段：Result契约 items/total
		err = my.buildResultWrap(ctx, u)
	default: // 列表关系：纯数组
		ctx.Write(`SELECT COALESCE(JSONB_AGG(TO_JSONB(`).
			Quote(`__sr_`, u.index).Write(`.*)), '[]') AS "json" FROM (`)
		err = my.buildCore(ctx, u, fieldsOf(u.field.SelectionSet), false)
		ctx.Write(`) AS `).Quote(`__sr_`, u.index)
	}
	if err != nil {
		return err
	}

	ctx.Write(`) AS `).Quote(`__sj_`, u.index).Write(` ON TRUE`)
	return nil
}

// buildResultWrap 根字段的 items/total 包装
func (my *Dialect) buildResultWrap(ctx *compiler.Context, u *unit) error {
	var items, typeNames []*ast.Field
	var hasTotal bool
	for _, f := range fieldsOf(u.field.SelectionSet) {
		switch f.Name {
		case protocol.ITEMS:
			items = fieldsOf(f.SelectionSet)
		case protocol.TOTAL:
			hasTotal = true
		case typename:
			typeNames = append(typeNames, f)
		}
	}
	if len(items) == 0 && !hasTotal {
		return fmt.Errorf("查询 %s 缺少items选择集", u.field.Name)
	}

	sr := func() *compiler.Context { return ctx.Quote(`__sr_`, u.index) }

	ctx.Write(`SELECT JSONB_BUILD_OBJECT('`, protocol.ITEMS, `', COALESCE(JSONB_AGG(TO_JSONB(`)
	sr().Write(`.*)`)
	if hasTotal {
		ctx.Write(` - '__total'`)
	}
	ctx.Write(`), '[]')`)
	if hasTotal {
		ctx.Write(`, '`, protocol.TOTAL, `', COALESCE(MIN(`)
		sr().Write(`."__total"), 0)`)
	}
	for _, f := range typeNames {
		ctx.Write(`, '`, f.Alias, `', '`, u.class.Name, protocol.SUFFIX_RESULT, `'`)
	}
	ctx.Write(`) AS "json" FROM (`)

	if err := my.buildCore(ctx, u, items, hasTotal); err != nil {
		return err
	}

	ctx.Write(`) AS `)
	sr()
	return nil
}

// buildCore 单元核心：列投影 + 基础查询(条件/排序/分页) + 子关系LATERAL
//
//	SELECT "base"."col" AS "字段别名", "__sj_M"."json" AS "关系别名"
//	FROM (SELECT 原始列 FROM 表 [JOIN 中间表] WHERE 关联+条件 ORDER LIMIT) AS "base"
//	LEFT OUTER JOIN LATERAL (子单元) AS "__sj_M" ON TRUE
func (my *Dialect) buildCore(ctx *compiler.Context, u *unit, selection []*ast.Field, withTotal bool) error {
	base := fmt.Sprintf("%s_%d", u.class.Table, u.index)
	sc := scope{class: u.class, qualifier: u.class.Table}
	ctx.MarkTable(u.class.Table)

	// 分拣标量列与子关系，并收集基础查询所需的原始列
	type relIndex struct {
		unit  *unit
		alias string
	}
	var scalars, typeNames []*ast.Field
	var children []relIndex
	columns := make([]string, 0, len(selection))
	seen := make(map[string]bool)
	appendColumn := func(column string) {
		if column != "" && !seen[column] {
			seen[column] = true
			columns = append(columns, column)
		}
	}

	for _, f := range selection {
		if f.Name == typename {
			typeNames = append(typeNames, f)
			continue
		}
		field, ok := u.class.Fields[f.Name]
		if !ok || strings.HasPrefix(f.Name, "__") {
			continue
		}
		// 关系遍历字段：无列的虚拟字段且携带关系（FK/主键列虽带Relation但仍是标量）
		if field.Column == "" && field.Relation != nil {
			target, ok := ctx.GetClass(field.Relation.TargetClass)
			if !ok {
				return fmt.Errorf("关系目标类不存在: %s", field.Relation.TargetClass)
			}
			children = append(children, relIndex{
				unit: &unit{
					field: f, class: target, rel: field.Relation, parent: base,
					index: ctx.NextIndex(), single: f.Definition.Type.NamedType != "", args: f.Arguments,
				},
				alias: f.Alias,
			})
			// 子关系的关联条件引用父级源列，基础查询必须带出
			appendColumn(sc.column(field.Relation.SourceFiled))
			continue
		}
		if field.Column == "" {
			continue // 虚拟字段（resolver等）不参与SQL
		}
		scalars = append(scalars, f)
		appendColumn(field.Column)
	}
	for _, column := range sortColumns(sc, u.args) {
		appendColumn(column)
	}
	if len(columns) == 0 {
		return fmt.Errorf("查询 %s 没有可用的标量字段", u.field.Name)
	}

	// 列投影：原始列 -> GraphQL字段别名，__typename -> 类型名字面量，子关系 -> json别名
	written := 0
	comma := func() {
		if written > 0 {
			ctx.SpaceAfter(`,`)
		}
		written++
	}
	ctx.SpaceAfter(`SELECT`)
	for _, f := range scalars {
		comma()
		ctx.Quote(base).Write(`.`).Quote(sc.column(f.Name)).Space(`AS`).Quote(f.Alias)
	}
	for _, f := range typeNames {
		comma()
		ctx.Write(`'`, u.class.Name, `' AS `).Quote(f.Alias)
	}
	if withTotal {
		comma()
		ctx.Quote(base).Write(`."__total"`)
	}
	for _, child := range children {
		comma()
		ctx.Quote(`__sj_`, child.unit.index).Write(`."json"`).Space(`AS`).Quote(child.alias)
	}

	// 基础查询
	ctx.Space(`FROM (SELECT`)
	for i, column := range columns {
		if i > 0 {
			ctx.SpaceAfter(`,`)
		}
		ctx.Quote(u.class.Table).Write(`.`).Quote(column)
	}
	if withTotal {
		ctx.SpaceAfter(`,`).Write(`COUNT(*) OVER() AS "__total"`)
	}
	ctx.Space(`FROM`).Write(u.class.Table)

	bond, err := my.relationBond(ctx, u, sc)
	if err != nil {
		return err
	}
	if err = my.buildWhere(ctx, sc, u.args, bond); err != nil {
		return err
	}
	if err = my.buildOrderBy(ctx, sc, u.args); err != nil {
		return err
	}
	if err = my.buildLimit(ctx, u); err != nil {
		return err
	}
	ctx.Write(`) AS `).Quote(base)

	// 子关系单元
	for _, child := range children {
		if err := my.buildUnit(ctx, child.unit); err != nil {
			return err
		}
	}
	return nil
}

// relationBond 生成父子关联条件；多对多在基础查询追加中间表JOIN后返回关联闭包
func (my *Dialect) relationBond(ctx *compiler.Context, u *unit, sc scope) (func(), error) {
	if u.rel == nil {
		return nil, nil
	}

	parentClass, ok := ctx.GetClass(u.rel.SourceClass)
	if !ok {
		return nil, fmt.Errorf("关系源类不存在: %s", u.rel.SourceClass)
	}
	parentCol := scope{class: parentClass}.column(u.rel.SourceFiled)
	targetCol := sc.column(u.rel.TargetFiled)

	if through := u.rel.Through; through != nil {
		// 中间表JOIN：中间表.目标键 = 目标表.目标列
		ctx.MarkTable(through.TableName)
		ctx.Space(`INNER JOIN`).Write(through.TableName).
			Space(`ON`).Quote(through.TableName).Write(`.`).Quote(through.TargetKey).
			Space(`=`).Quote(u.class.Table).Write(`.`).Quote(targetCol)
		// 关联条件：中间表.源键 = 父别名.源列
		return func() {
			ctx.Quote(through.TableName).Write(`.`).Quote(through.SourceKey).
				Space(`=`).Quote(u.parent).Write(`.`).Quote(parentCol)
		}, nil
	}

	// 普通关联：目标表.目标列 = 父别名.源列
	return func() {
		ctx.Quote(u.class.Table).Write(`.`).Quote(targetCol).
			Space(`=`).Quote(u.parent).Write(`.`).Quote(parentCol)
	}, nil
}

// buildLimit 构建LIMIT/OFFSET：单对象单元固定LIMIT 1，字面量内联，变量走参数槽位
func (my *Dialect) buildLimit(ctx *compiler.Context, u *unit) error {
	if u.single {
		ctx.Space(`LIMIT 1`)
		return nil
	}

	for _, name := range []string{protocol.LIMIT, protocol.OFFSET} {
		arg := u.args.ForName(name)
		if arg == nil || arg.Value == nil {
			continue
		}
		ctx.Space(strings.ToUpper(name))
		if arg.Value.Kind == ast.Variable {
			ctx.Write(my.Placeholder(ctx.AddVariable(arg.Value.Raw)))
			continue
		}
		val, err := arg.Value.Value(nil)
		if err != nil {
			return fmt.Errorf("解析%s参数失败: %w", name, err)
		}
		count, ok := val.(int64)
		if !ok || count < 0 {
			return fmt.Errorf("%s必须是非负整数，得到 %v", name, val)
		}
		ctx.Write(int(count))
	}

	return nil
}

// fieldsOf 提取选择集中的字段列表
func fieldsOf(set ast.SelectionSet) []*ast.Field {
	fields := make([]*ast.Field, 0, len(set))
	for _, s := range set {
		if f, ok := s.(*ast.Field); ok {
			fields = append(fields, f)
		}
	}
	return fields
}
