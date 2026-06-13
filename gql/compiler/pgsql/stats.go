// Package pgsql 统计聚合编译模块
// userStats(where, groupBy, limit, offset): [UserStats!]! 编译为选择驱动的聚合查询：
// 选了哪个字段/哪个聚合才生成对应表达式，groupBy生成分组键对象与GROUP BY子句
package pgsql

import (
	"fmt"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/protocol"
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
	fields := fieldsOf(u.field.SelectionSet)
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
			for i, sub := range fieldsOf(f.SelectionSet) {
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
	ctx.Space(`FROM`).Write(u.class.Table)
	if err = my.buildWhere(ctx, sc, u.args); err != nil {
		return err
	}
	if len(groups) > 0 {
		ctx.Space(`GROUP BY`)
		writeColumns(ctx, u.class.Table, columnsOf(sc, groups))
	}
	return my.buildLimit(ctx, u)
}
