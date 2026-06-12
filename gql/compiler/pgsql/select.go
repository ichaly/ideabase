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
	stats  bool               // 统计聚合形态（xxxStats根字段）
	page   *pager             // 游标分页参数（first/last模式）
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

		typeName := field.Definition.Type.Name()
		stats := strings.HasSuffix(typeName, protocol.SUFFIX_STATS)
		className := strings.TrimSuffix(strings.TrimSuffix(typeName, protocol.SUFFIX_RESULT), protocol.SUFFIX_STATS)
		class, ok := ctx.GetClass(className)
		if !ok {
			return fmt.Errorf("不支持的根查询字段: %s", field.Name)
		}
		u := &unit{field: field, class: class, index: ctx.NextIndex(), args: field.Arguments, stats: stats}
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
	case u.stats: // 统计聚合
		err = my.buildStatsWrap(ctx, u)
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

// buildResultWrap 根字段的 items/total/pageInfo 包装
func (my *Dialect) buildResultWrap(ctx *compiler.Context, u *unit) error {
	var items, typeNames []*ast.Field
	var pageInfo *ast.Field
	var hasTotal bool
	for _, f := range fieldsOf(u.field.SelectionSet) {
		switch f.Name {
		case protocol.ITEMS:
			items = fieldsOf(f.SelectionSet)
		case protocol.TOTAL:
			hasTotal = true
		case protocol.PAGE_INFO:
			pageInfo = f
		case typename:
			typeNames = append(typeNames, f)
		}
	}
	if len(items) == 0 && !hasTotal {
		return fmt.Errorf("查询 %s 缺少items选择集", u.field.Name)
	}

	page, err := newPager(scope{class: u.class}, u.args)
	if err != nil {
		return err
	}
	u.page = page
	switch {
	case pageInfo != nil && page == nil:
		return fmt.Errorf("pageInfo需要配合first/last使用")
	case hasTotal && page != nil && page.cursor != nil:
		return fmt.Errorf("total不能与after/before续页同用（边界后计数无总数语义）")
	}

	sr := func() *compiler.Context { return ctx.Quote(`__sr_`, u.index) }

	// items聚合：游标模式剔除辅助列、按行号FILTER并保持显示顺序
	ctx.Write(`SELECT JSONB_BUILD_OBJECT('`, protocol.ITEMS, `', COALESCE(JSONB_AGG(`)
	if page == nil {
		ctx.Write(`TO_JSONB(`)
		sr().Write(`.*)`)
		if hasTotal {
			ctx.Write(` - '__total'`)
		}
	} else {
		ctx.Write(`(TO_JSONB(`)
		sr().Write(`.*) - '__rn' - '__cursor'`)
		if hasTotal {
			ctx.Write(` - '__total'`)
		}
		ctx.Write(`) ORDER BY `)
		sr().Write(`."__rn"`)
		if page.last {
			ctx.Write(` DESC`)
		}
		ctx.Write(`) FILTER (WHERE `)
		sr().Write(`."__rn" <= `, page.limit)
	}
	ctx.Write(`), '[]')`)

	if hasTotal {
		ctx.Write(`, '`, protocol.TOTAL, `', COALESCE(MIN(`)
		sr().Write(`."__total"), 0)`)
	}
	if pageInfo != nil {
		ctx.Write(`, '`, pageInfo.Alias, `', `)
		my.buildPageInfo(ctx, u, pageInfo)
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

// buildPageInfo 游标分页信息：N+1探测行决定hasNext/hasPrev，边界行游标为start/end
func (my *Dialect) buildPageInfo(ctx *compiler.Context, u *unit, field *ast.Field) {
	page := u.page
	sr := func() *compiler.Context { return ctx.Quote(`__sr_`, u.index) }
	// 探测：取到的行数超过N说明边界外还有数据
	probe := func() {
		ctx.Write(`COALESCE(MAX(`)
		sr().Write(`."__rn") > `, page.limit)
		ctx.Write(`, FALSE)`)
	}
	// 显示顺序的游标聚合，->>0 首条 ->>-1 末条
	boundary := func(index int) {
		ctx.Write(`(JSONB_AGG(`)
		sr().Write(`."__cursor" ORDER BY `)
		sr().Write(`."__rn"`)
		if page.last {
			ctx.Write(` DESC`)
		}
		ctx.Write(`) FILTER (WHERE `)
		sr().Write(`."__rn" <= `, page.limit)
		ctx.Write(`) ->> `, index, `)`)
	}

	ctx.Write(`JSONB_BUILD_OBJECT(`)
	for i, f := range fieldsOf(field.SelectionSet) {
		if i > 0 {
			ctx.Write(`, `)
		}
		ctx.Write(`'`, f.Alias, `', `)
		switch f.Name {
		case typename:
			ctx.Write(`'`, protocol.TYPE_PAGE_INFO, `'`)
		case "hasNext":
			if page.last {
				my.boundaryGiven(ctx, page)
			} else {
				probe()
			}
		case "hasPrev":
			if page.last {
				probe()
			} else {
				my.boundaryGiven(ctx, page)
			}
		case "start":
			boundary(0)
		case "end":
			boundary(-1)
		}
	}
	ctx.Write(`)`)
}

// boundaryGiven 是否提供了续页游标：字面量编译期定值，变量运行期判空
func (my *Dialect) boundaryGiven(ctx *compiler.Context, page *pager) {
	if page.cursor == nil {
		ctx.Write(false)
		return
	}
	if page.cursor.Kind == ast.Variable {
		ctx.Write(`(`, my.Placeholder(ctx.AddVariable(page.cursor.Raw)), `::text IS NOT NULL)`)
		return
	}
	ctx.Write(true)
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
	if u.page != nil {
		for _, key := range u.page.keys {
			appendColumn(key.column)
		}
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
	if u.page != nil {
		// 行号探测hasNext，行级游标=base64(排序键值JSON数组)
		comma()
		ctx.Write(`ROW_NUMBER() OVER () AS "__rn"`)
		comma()
		ctx.Write(`encode(convert_to(JSONB_BUILD_ARRAY(`)
		for i, key := range u.page.keys {
			if i > 0 {
				ctx.Write(`, `)
			}
			ctx.Quote(base).Write(`.`).Quote(key.column)
		}
		ctx.Write(`)::text, 'UTF8'), 'base64') AS "__cursor"`)
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
	// 全文搜索条件并入关联条件位
	search, err := newSearcher(ctx, sc, u.args)
	if err != nil {
		return err
	}
	var bondErr error
	if search != nil {
		if u.page != nil && len(sortEntries(u.args)) == 0 {
			return fmt.Errorf("search与游标分页同用时必须显式sort（相关度排序无法作为稳定游标键）")
		}
		prev := bond
		bond = func() {
			if prev != nil {
				prev()
				ctx.Space(`AND`)
			}
			bondErr = search.buildCondition(my, ctx, sc)
		}
	}
	// 游标续页：keyset边界条件并入关联条件位
	if u.page != nil && u.page.cursor != nil {
		prev := bond
		bond = func() {
			if prev != nil {
				prev()
				ctx.Space(`AND`)
			}
			if bondErr == nil {
				bondErr = my.buildKeyset(ctx, sc, u.page)
			}
		}
	}
	if err = my.buildWhere(ctx, sc, u.args, bond); err != nil {
		return err
	}
	if bondErr != nil {
		return bondErr
	}

	if u.page != nil {
		// 排序键全序（向后翻页方向反转），取N+1行探测边界
		ctx.Space(`ORDER BY`)
		for i, key := range u.page.keys {
			if i > 0 {
				ctx.Write(`, `)
			}
			ctx.Quote(sc.qualifier).Write(`.`).Quote(key.column).SpaceBefore(u.page.order(i))
		}
		ctx.Space(`LIMIT`).Write(u.page.limit + 1)
	} else {
		// 无显式排序时按搜索相关度降序
		if search != nil && len(sortEntries(u.args)) == 0 {
			ctx.Space(`ORDER BY`)
			if _, err = search.buildRank(my, ctx, sc); err != nil {
				return err
			}
		} else if err = my.buildOrderBy(ctx, sc, u.args); err != nil {
			return err
		}
		if err = my.buildLimit(ctx, u); err != nil {
			return err
		}
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
