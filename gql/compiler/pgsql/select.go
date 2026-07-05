// Package pgsql 实现PostgreSQL的SQL方言
// SELECT编译遵循 doc/pgsql-template-design.md：
// 每个实体字段编译为一个LATERAL JOIN子查询单元，关联条件由关系元数据驱动，
// 嵌套任意深度仍是单条SQL，从根上消除N+1
package pgsql

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2/ast"
)

// shape 单元的JSON包装形态
type shape int

const (
	shapeResult shape = iota // 查询根字段：Result契约 items/total/pageInfo
	shapeSingle              // 单对象：多对一关系、单条变更读回
	shapeList                // 纯数组：列表关系、批量/upsert读回
	shapeStats               // 统计聚合
)

// unit 一个LATERAL JOIN子查询单元：查询根字段、关系字段或变更读回
type unit struct {
	field    *ast.Field         // GraphQL字段
	class    *protocol.Class    // 对应实体
	rel      *protocol.Relation // 与父级的关系，根字段为nil
	parent   string             // 父级基表别名（lateral关联引用）
	index    int                // 单元序号，决定 __sj_N/__sr_N 别名
	shape    shape              // JSON包装形态
	page     *pager             // 游标分页参数（first/last模式）
	args     ast.ArgumentList   // 生效的查询参数；变更读回为nil（参数已被CTE消费）
	readback bool               // 变更读回顶层单元（读CTE非基表），跳过行级作用域注入
	ordered  bool               // 显式排序的非游标列表：__rn行号在聚合内固化顺序
}

// orderedList 是否携带显式排序语义：sort参数或search相关度（JSONB_AGG不保证
// 维持输入序，并行/归并计划下会丢失，须经__rn在聚合内ORDER BY固化）
func orderedList(args ast.ArgumentList) bool {
	return len(sortEntries(args)) > 0 || args.ForName(protocol.SEARCH) != nil
}

// BuildQuery 构建查询语句：根JSON对象 + 每个根字段一个LATERAL单元
func (my *Dialect) BuildQuery(ctx *compiler.Context, set ast.SelectionSet) error {
	fields := compiler.FieldsOf(set)
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
		kind := shapeResult
		if strings.HasSuffix(typeName, protocol.SUFFIX_STATS) {
			kind = shapeStats
		}
		className := strings.TrimSuffix(strings.TrimSuffix(typeName, protocol.SUFFIX_RESULT), protocol.SUFFIX_STATS)
		class, ok := ctx.GetClass(className)
		if !ok {
			return fmt.Errorf("不支持的根查询字段: %s", field.Name)
		}
		u := &unit{field: field, class: class, index: ctx.NextIndex(), args: field.Arguments, shape: kind}
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
	switch u.shape {
	case shapeSingle:
		ctx.Write(`SELECT TO_JSONB(`).
			Quote(`__sr_`, u.index).Write(`.*) AS "json" FROM (`)
		err = my.buildCore(ctx, u, compiler.FieldsOf(u.field.SelectionSet), false)
		ctx.Write(`) AS `).Quote(`__sr_`, u.index)
	case shapeResult:
		err = my.buildResultWrap(ctx, u)
	default: // shapeList / shapeStats：纯数组包装
		core := func() error { return my.buildCore(ctx, u, compiler.FieldsOf(u.field.SelectionSet), false) }
		if u.shape == shapeStats {
			core = func() error { return my.buildStatsCore(ctx, u) }
		} else {
			u.ordered = orderedList(u.args)
		}
		ctx.Write(`SELECT COALESCE(JSONB_AGG(TO_JSONB(`).
			Quote(`__sr_`, u.index).Write(`.*)`)
		if u.ordered {
			ctx.Write(` - '__rn' ORDER BY `).Quote(`__sr_`, u.index).Write(`."__rn"`)
		}
		ctx.Write(`), '[]') AS "json" FROM (`)
		err = core()
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
	for _, f := range compiler.FieldsOf(u.field.SelectionSet) {
		switch f.Name {
		case protocol.ITEMS:
			items = compiler.FieldsOf(f.SelectionSet)
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
	next := comma(ctx)

	// 响应只含选择的字段：items未请求（仅total）时不输出
	ctx.Write(`SELECT JSONB_BUILD_OBJECT(`)
	u.ordered = page == nil && orderedList(u.args)
	if len(items) > 0 {
		next()
		// items聚合：游标模式剔除辅助列、按行号FILTER并保持显示顺序
		ctx.Write(`'`, protocol.ITEMS, `', COALESCE(JSONB_AGG(`)
		if page == nil {
			ctx.Write(`TO_JSONB(`)
			sr().Write(`.*)`)
			if u.ordered {
				ctx.Write(` - '__rn'`)
			}
			if hasTotal {
				ctx.Write(` - '__total'`)
			}
			if u.ordered { // 显式排序经__rn在聚合内固化（JSONB_AGG不保证维持输入序）
				ctx.Write(` ORDER BY `)
				sr().Write(`."__rn"`)
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
			sr().Write(`."__rn" <= `)
			page.writeSize(ctx, my, 0)
		}
		ctx.Write(`), '[]')`)
	}

	if hasTotal {
		next()
		ctx.Write(`'`, protocol.TOTAL, `', COALESCE(MIN(`)
		sr().Write(`."__total"), 0)`)
	}
	if pageInfo != nil {
		next()
		ctx.Write(`'`, pageInfo.Alias, `', `)
		my.buildPageInfo(ctx, u, pageInfo)
	}
	for _, f := range typeNames {
		next()
		ctx.Write(`'`, f.Alias, `', '`, u.class.Name, protocol.SUFFIX_RESULT, `'`)
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
		sr().Write(`."__rn") > `)
		page.writeSize(ctx, my, 0)
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
		sr().Write(`."__rn" <= `)
		page.writeSize(ctx, my, 0)
		ctx.Write(`) ->> `, index, `)`)
	}

	ctx.Write(`JSONB_BUILD_OBJECT(`)
	for i, f := range compiler.FieldsOf(field.SelectionSet) {
		if i > 0 {
			ctx.Write(`, `)
		}
		ctx.Write(`'`, f.Alias, `', `)
		switch f.Name {
		case typename:
			ctx.Write(`'`, protocol.TYPE_PAGE_INFO, `'`)
		case protocol.HAS_NEXT, protocol.HAS_PREV:
			// hasNext探测正向边界、hasPrev探测反向；last模式两者语义对调
			if (f.Name == protocol.HAS_NEXT) != page.last {
				probe()
			} else {
				my.boundaryGiven(ctx, page)
			}
		case protocol.START:
			boundary(0)
		case protocol.END:
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
	sorts := sortEntries(u.args) // 单元内一次解析，列收集/排序/校验共用
	ctx.MarkTable(u.class.Table)

	// 分拣标量列与子关系，并收集基础查询所需的原始列
	type relIndex struct {
		unit  *unit
		alias string
	}
	var scalars, typeNames []*ast.Field
	var children []relIndex
	columns := make([]string, 0, len(selection))
	appendColumn := func(column string) {
		if column == "" {
			return
		}
		for _, c := range columns { // 列集通常≤16，线性去重免map分配
			if c == column {
				return
			}
		}
		columns = append(columns, column)
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
					index: ctx.NextIndex(), shape: childShape(f), args: f.Arguments,
				},
				alias: f.Alias,
			})
			// 子关系的关联条件引用父级源列，基础查询必须带出
			appendColumn(sc.column(field.Relation.SourceField))
			continue
		}
		if field.Remote != nil {
			// 远程关系字段不落SQL，但把宿主键列以内部别名补进投影：
			// 执行期批量取数依赖它，别名避开选择集与codec路径保证键值原始，回填后剥除
			alias := field.Remote.Alias()
			if !hasAlias(scalars, alias) {
				scalars = append(scalars, &ast.Field{Name: field.Remote.Key, Alias: alias})
				appendColumn(sc.column(field.Remote.Key))
			}
			continue
		}
		if field.Column == "" {
			continue // 虚拟字段（resolver等）不参与SQL
		}
		scalars = append(scalars, f)
		appendColumn(field.Column)
	}
	for _, column := range sortColumns(sc, sorts) {
		appendColumn(column)
	}
	distinctNames, err := fieldNames(sc, u.args, protocol.DISTINCT)
	if err != nil {
		return err
	}
	distinct := columnsOf(sc, distinctNames)
	if len(distinct) > 0 {
		if u.page != nil {
			return fmt.Errorf("distinct与游标分页不能同时使用")
		}
		if withTotal {
			// COUNT(*) OVER()在DISTINCT ON去重前求值，total会是去重前行数（静默错数）
			return fmt.Errorf("distinct与total不能同时使用")
		}
		for _, column := range distinct {
			appendColumn(column)
		}
	}
	if u.page != nil {
		for _, key := range u.page.keys {
			appendColumn(key.column)
		}
	}
	if len(columns) == 0 && !withTotal {
		return fmt.Errorf("查询 %s 没有可用的标量字段", u.field.Name)
	}

	// 列投影：原始列 -> GraphQL字段别名，__typename -> 类型名字面量，子关系 -> json别名
	next := comma(ctx)
	ctx.SpaceAfter(`SELECT`)
	for _, f := range scalars {
		next()
		ctx.Column(base, sc.column(f.Name)).Space(`AS`).Quote(f.Alias)
	}
	for _, f := range typeNames {
		next()
		ctx.Write(`'`, u.class.Name, `' AS `).Quote(f.Alias)
	}
	if withTotal {
		next()
		ctx.Quote(base).Write(`."__total"`)
	}
	if u.ordered {
		// 显式排序的行号：聚合内ORDER BY固化顺序后剥除
		next()
		ctx.Write(`ROW_NUMBER() OVER () AS "__rn"`)
	}
	if u.page != nil {
		// 行号探测hasNext，行级游标=base64(排序键值JSON数组)
		next()
		ctx.Write(`ROW_NUMBER() OVER () AS "__rn"`)
		next()
		ctx.Write(`encode(convert_to(JSONB_BUILD_ARRAY(`)
		for i, key := range u.page.keys {
			if i > 0 {
				ctx.Write(`, `)
			}
			ctx.Column(base, key.column)
		}
		ctx.Write(`)::text, 'UTF8'), 'base64') AS "__cursor"`)
	}
	for _, child := range children {
		next()
		ctx.Quote(`__sj_`, child.unit.index).Write(`."json"`).Space(`AS`).Quote(child.alias)
	}

	// 深度递归字段：基础查询为递归CTE全树遍历
	if u.rel != nil && u.rel.Deep {
		if err := my.buildTree(ctx, u, sc, columns); err != nil {
			return err
		}
		ctx.Write(`) AS `).Quote(base)
		for _, child := range children {
			if err := my.buildUnit(ctx, child.unit); err != nil {
				return err
			}
		}
		return nil
	}

	// 基础查询；DISTINCT ON要求排序以去重列开头（PG规则）
	ctx.Space(`FROM (SELECT`)
	if len(distinct) > 0 {
		ctx.Write(` DISTINCT ON (`)
		writeColumns(ctx, u.class.Table, distinct)
		ctx.Write(`) `)
	}
	writeColumns(ctx, u.class.Table, columns)
	if withTotal {
		if len(columns) > 0 {
			ctx.SpaceAfter(`,`)
		}
		ctx.Write(`COUNT(*) OVER() AS "__total"`)
	}
	ctx.Space(`FROM`)
	if u.readback {
		ctx.Quote(u.class.Table) // 读回单元：裸名命中同名变更CTE
	} else {
		tableRef(ctx, u.class.Table)
	}

	// WHERE位的合取条件：父子关联 + 全文搜索 + keyset续页边界
	conjuncts, err := my.relationBond(ctx, u, sc)
	if err != nil {
		return err
	}
	search, err := newSearcher(ctx, sc, u.args)
	if err != nil {
		return err
	}
	if search != nil {
		if u.page != nil && len(sorts) == 0 {
			return fmt.Errorf("search与游标分页同用时必须显式sort（相关度排序无法作为稳定游标键）")
		}
		conjuncts = append(conjuncts, func() error { return search.buildCondition(my, ctx, sc) })
	}
	if u.page != nil && u.page.cursor != nil {
		conjuncts = append(conjuncts, func() error { return my.buildKeyset(ctx, sc, u.page) })
	}
	// 行级作用域：实体声明scope时强制注入 列=上下文值（租户/属主隔离）；
	// 读基表的查询单元才注入，变更读回顶层单元读CTE跳过
	if !u.readback {
		conjuncts = append(conjuncts, my.scopeConjuncts(ctx, sc.qualifier, u.class)...)
	}
	if err = my.buildWhere(ctx, sc, u.args, conjuncts...); err != nil {
		return err
	}

	if u.page != nil {
		// 排序键全序（向后翻页方向反转），取N+1行探测边界
		ctx.Space(`ORDER BY`)
		for i, key := range u.page.keys {
			if i > 0 {
				ctx.Write(`, `)
			}
			ctx.Column(sc.qualifier, key.column).SpaceBefore(u.page.order(i))
		}
		ctx.Space(`LIMIT`)
		u.page.writeSize(ctx, my, 1)
	} else {
		switch {
		case len(distinct) > 0:
			// DISTINCT ON：去重列前置排序，用户sort追加其后
			if search != nil {
				return fmt.Errorf("distinct与search不能同时使用")
			}
			if err = my.buildOrderBy(ctx, sc, sorts, distinct...); err != nil {
				return err
			}
		// 无显式排序时按搜索相关度降序（ilike模式无相关度，维持自然序）
		case search != nil && len(sorts) == 0:
			if search.hasRank() {
				ctx.Space(`ORDER BY`)
				if err = search.buildRank(my, ctx, sc); err != nil {
					return err
				}
			}
		default:
			if err = my.buildOrderBy(ctx, sc, sorts); err != nil {
				return err
			}
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

// relationBond 生成父子关联条件（合取项）；多对多在基础查询追加中间表JOIN
func (my *Dialect) relationBond(ctx *compiler.Context, u *unit, sc scope) ([]func() error, error) {
	if u.rel == nil {
		return nil, nil
	}

	parentClass, ok := ctx.GetClass(u.rel.SourceClass)
	if !ok {
		return nil, fmt.Errorf("关系源类不存在: %s", u.rel.SourceClass)
	}
	parentCol := scope{class: parentClass}.column(u.rel.SourceField)
	targetCol := sc.column(u.rel.TargetField)

	if through := u.rel.Through; through != nil {
		// 中间表JOIN：中间表.目标键 = 目标表.目标列
		ctx.MarkTable(through.TableName)
		ctx.Space(`INNER JOIN`)
		tableRef(ctx, through.TableName)
		ctx.Space(`ON`).Column(through.TableName, through.TargetKey).
			Space(`=`).Column(u.class.Table, targetCol)
		// 关联条件：中间表.源键 = 父别名.源列
		return []func() error{func() error {
			ctx.Column(through.TableName, through.SourceKey).
				Space(`=`).Column(u.parent, parentCol)
			return nil
		}}, nil
	}

	// 普通关联：目标表.目标列 = 父别名.源列
	return []func() error{func() error {
		ctx.Column(u.class.Table, targetCol).
			Space(`=`).Column(u.parent, parentCol)
		return nil
	}}, nil
}

// childShape 嵌套关系字段的形态：命名类型为单对象，否则列表
func childShape(f *ast.Field) shape {
	if f.Definition.Type.NamedType != "" {
		return shapeSingle
	}
	return shapeList
}

// buildLimit 构建LIMIT/OFFSET：单对象单元固定LIMIT 1，字面量内联，变量走参数槽位
func (my *Dialect) buildLimit(ctx *compiler.Context, u *unit) error {
	if u.shape == shapeSingle {
		ctx.Space(`LIMIT 1`)
		return nil
	}

	limited := false
	for _, name := range []string{protocol.LIMIT, protocol.OFFSET} {
		arg := u.args.ForName(name)
		if arg == nil || arg.Value == nil {
			continue
		}
		if name == protocol.LIMIT {
			limited = true
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

	// 缺省LIMIT兜底：无显式limit的列表注入配置的default-limit，防无界全表扫描；
	// 变更读回须返回全部受影响行、统计分组保持完整语义、递归全树已有depth限深，均不注入
	if !limited && !u.readback && u.shape != shapeStats && (u.rel == nil || !u.rel.Deep) {
		if n := ctx.DefaultLimit(); n > 0 {
			ctx.Space(`LIMIT`).Write(n)
		}
	}
	return nil
}

// buildTree 深度递归基础查询：WITH RECURSIVE 全树遍历
//
//	(WITH RECURSIVE "__tree_N" AS (
//	   SELECT 列..., 1 AS "__lv" FROM 表 WHERE 表.目标列 = 父锚.源列      -- 起始层
//	   UNION ALL
//	   SELECT t.列..., "__tree_N"."__lv"+1 FROM 表 t, "__tree_N"
//	   WHERE t.目标列 = "__tree_N".源列 AND "__tree_N"."__lv" < 深度      -- 步进+限深
//	) SELECT 列... FROM "__tree_N" [WHERE 用户条件] [ORDER BY] [LIMIT])
func (my *Dialect) buildTree(ctx *compiler.Context, u *unit, sc scope, columns []string) error {
	depth, err := treeDepth(u.args)
	if err != nil {
		return err
	}

	// 递归引用需要源列与目标列
	need := map[string]bool{}
	for _, column := range columns {
		need[column] = true
	}
	sourceCol, targetCol := sc.column(u.rel.SourceField), sc.column(u.rel.TargetField)
	all := append([]string{}, columns...)
	for _, column := range []string{sourceCol, targetCol} {
		if !need[column] {
			need[column] = true
			all = append(all, column)
		}
	}

	tree := fmt.Sprintf("__tree_%d", u.index)
	parentClass, _ := ctx.GetClass(u.rel.SourceClass)
	parentCol := scope{class: parentClass}.column(u.rel.SourceField)

	list := func(qualifier string) { writeColumns(ctx, qualifier, all) }

	// 行级作用域：递归CTE的起始层与步进层都注入，递归只在本租户内遍历（限范围、防跨租户）
	scopeFilter := func() { my.appendScope(ctx, u.class.Table, u.class.Scope) }

	ctx.Space(`FROM (WITH RECURSIVE`).QuotedWithSpace(tree).Write(`AS (SELECT `)
	list(u.class.Table)
	ctx.Write(`, 1 AS "__lv" FROM `)
	tableRef(ctx, u.class.Table)
	ctx.Write(` WHERE `).
		Column(u.class.Table, targetCol).
		Write(` = `).Column(u.parent, parentCol)
	scopeFilter()
	ctx.Write(` UNION ALL SELECT `)
	list(u.class.Table)
	ctx.Write(`, `).Quote(tree).Write(`."__lv" + 1 FROM `)
	tableRef(ctx, u.class.Table)
	ctx.Write(`, `).Quote(tree).
		Write(` WHERE `).Column(u.class.Table, targetCol).
		Write(` = `).Column(tree, sourceCol).
		Write(` AND `).Quote(tree).Write(`."__lv" < `, depth)
	scopeFilter()
	ctx.Write(`) SELECT `)
	list(tree)
	ctx.Write(` FROM `).Quote(tree)

	// 用户条件/排序/分页应用在递归完成后的结果集上
	treeScope := scope{class: u.class, qualifier: tree}
	if err = my.buildWhere(ctx, treeScope, u.args); err != nil {
		return err
	}
	if err = my.buildOrderBy(ctx, treeScope, sortEntries(u.args)); err != nil {
		return err
	}
	return my.buildLimit(ctx, u)
}

// treeDepth 解析depth参数：1..32的字面量，缺省5（限深防爆炸）
func treeDepth(args ast.ArgumentList) (int, error) {
	arg := args.ForName(protocol.DEPTH)
	if arg == nil || arg.Value == nil {
		return 5, nil
	}
	if arg.Value.Kind == ast.Variable {
		return 0, fmt.Errorf("depth必须是字面量整数")
	}
	depth, err := strconv.Atoi(arg.Value.Raw)
	if err != nil || depth < 1 || depth > 32 {
		return 0, fmt.Errorf("depth必须在1..32之间")
	}
	return depth, nil
}
