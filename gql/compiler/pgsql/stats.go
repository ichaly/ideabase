// Package pgsql 统计聚合编译模块
// userStats(where, groupBy, limit, offset): [UserStats!]! 编译为选择驱动的聚合查询：
// 选了哪个字段/哪个聚合才生成对应表达式，groupBy生成分组键对象与GROUP BY子句
package pgsql

import (
	"fmt"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2/ast"
)

// aggregates 聚合子字段到SQL函数调用前缀（统一"前缀(列)"形式）
var aggregates = map[string]string{
	protocol.FUNCTION_SUM:            "SUM(",
	protocol.FUNCTION_AVG:            "AVG(",
	protocol.FUNCTION_MIN:            "MIN(",
	protocol.FUNCTION_MAX:            "MAX(",
	protocol.FUNCTION_COUNT_DISTINCT: "COUNT(DISTINCT ",
}

// buildStatsCore 聚合核心：投影 + WHERE + GROUP BY + LIMIT
func (my *Dialect) buildStatsCore(ctx *compiler.Context, u *unit) error {
	sc := scope{class: u.class, qualifier: u.class.Table}
	ctx.MarkTable(u.class.Table)

	groups, err := fieldNames(sc, u.args, protocol.GROUP_BY)
	if err != nil {
		return err
	}

	// 投影：选择驱动，逐字段生成聚合表达式
	column := func(name string) {
		ctx.Column(u.class.Table, sc.column(name))
	}
	fields := compiler.FieldsOf(u.field.SelectionSet)
	if len(fields) == 0 {
		return fmt.Errorf("统计查询 %s 选择集为空", u.field.Name)
	}
	next := comma(ctx)
	ctx.SpaceAfter(`SELECT`)
	for _, f := range fields {
		next()
		switch f.Name {
		case typename:
			ctx.Write(`'`, u.class.Name, protocol.SUFFIX_STATS, `' AS `).Quote(f.Alias)
		case protocol.FUNCTION_KEY:
			// 分组键对象；无groupBy时为null
			if len(groups) == 0 {
				ctx.Write(`NULL AS `).Quote(f.Alias)
				continue
			}
			ctx.Write(`JSONB_BUILD_OBJECT(`)
			for i, name := range groups {
				if i > 0 {
					ctx.Write(`, `)
				}
				ctx.Write(`'`, name, `', `)
				column(name)
			}
			ctx.Write(`) AS `).Quote(f.Alias)
		case protocol.FUNCTION_COUNT:
			ctx.Write(`COUNT(*) AS `).Quote(f.Alias)
		default:
			field, ok := u.class.Fields[f.Name]
			if !ok || field.Column == "" {
				return fmt.Errorf("统计查询不支持字段: %s", f.Name)
			}
			// 字段级聚合对象：按子选择生成各聚合函数
			ctx.Write(`JSONB_BUILD_OBJECT(`)
			for i, sub := range compiler.FieldsOf(f.SelectionSet) {
				if i > 0 {
					ctx.Write(`, `)
				}
				if sub.Name == typename {
					ctx.Write(`'`, sub.Alias, `', '`, f.Definition.Type.Name(), `'`)
					continue
				}
				function, ok := aggregates[sub.Name]
				if !ok {
					return fmt.Errorf("不支持的聚合函数: %s", sub.Name)
				}
				ctx.Write(`'`, sub.Alias, `', `, function)
				column(f.Name)
				ctx.Write(`)`)
			}
			ctx.Write(`) AS `).Quote(f.Alias)
		}
	}
	ctx.Space(`FROM`).Quote(u.class.Table)
	// 行级作用域：聚合也强制隔离，否则 count/sum 泄露全表跨租户统计
	if err = my.buildWhere(ctx, sc, u.args, my.scopeConjuncts(ctx, sc.qualifier, u.class)...); err != nil {
		return err
	}
	if len(groups) > 0 {
		ctx.Space(`GROUP BY`)
		writeColumns(ctx, u.class.Table, columnsOf(sc, groups))
	}
	if err = my.buildHaving(ctx, sc, u.args); err != nil {
		return err
	}
	return my.buildLimit(ctx, u)
}

// buildHaving 构建HAVING子句：聚合表达式作为左值，复用where的操作符引擎。
// having: { count: {gt:N}, 列: { sum: {gt:X}, avg: {ge:Y} } }
// → HAVING COUNT(*) > $1 AND SUM("列") > $2 AND AVG("列") >= $3
// 聚合在数据库内过滤，只返回符合条件的分组，无应用层开销
func (my *Dialect) buildHaving(ctx *compiler.Context, sc scope, args ast.ArgumentList) error {
	arg := args.ForName(protocol.HAVING)
	if arg == nil || arg.Value == nil || len(arg.Value.Children) == 0 {
		return nil
	}

	first := true
	emit := func(lhs func(), ops *ast.Value) error {
		if ops == nil {
			return fmt.Errorf("having条件缺少操作符")
		}
		for _, op := range ops.Children {
			if first {
				ctx.Space(`HAVING`)
			} else {
				ctx.Space(`AND`)
			}
			first = false
			if err := my.buildOperator(ctx, lhs, op); err != nil {
				return err
			}
		}
		return nil
	}

	for _, child := range arg.Value.Children {
		if child.Name == protocol.FUNCTION_COUNT {
			if err := emit(func() { ctx.Write(`COUNT(*)`) }, child.Value); err != nil {
				return err
			}
			continue
		}
		// 列的各聚合：sum/avg/min/max/countDistinct
		field, ok := sc.class.Fields[child.Name]
		if !ok || field.Column == "" {
			return fmt.Errorf("having不支持字段: %s", child.Name)
		}
		col := field.Column
		if child.Value == nil {
			return fmt.Errorf("having字段 %s 缺少聚合条件", child.Name)
		}
		for _, agg := range child.Value.Children {
			prefix, ok := aggregates[agg.Name]
			if !ok {
				return fmt.Errorf("having不支持的聚合函数: %s", agg.Name)
			}
			lhs := func() { ctx.Write(prefix); ctx.Column(sc.qualifier, col); ctx.Write(`)`) }
			if err := emit(lhs, agg.Value); err != nil {
				return err
			}
		}
	}
	return nil
}
