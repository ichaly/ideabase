// Package pgsql 统计聚合编译模块
// userStats(where, groupBy, limit, offset): [UserStats!]! 编译为选择驱动的聚合查询：
// 选了哪个字段/哪个聚合才生成对应表达式，groupBy生成分组键对象与GROUP BY子句
package pgsql

import (
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2/ast"
)

// aggregates 聚合子字段到SQL函数的映射
var aggregates = map[string]string{
	protocol.FUNCTION_SUM:            "SUM",
	protocol.FUNCTION_AVG:            "AVG",
	protocol.FUNCTION_MIN:            "MIN",
	protocol.FUNCTION_MAX:            "MAX",
	protocol.FUNCTION_COUNT_DISTINCT: "COUNT(DISTINCT %s)",
}

// buildStatsWrap 统计单元：纯数组包装 + 聚合核心
func (my *Dialect) buildStatsWrap(ctx *compiler.Context, u *unit) error {
	ctx.Write(`SELECT COALESCE(JSONB_AGG(TO_JSONB(`).
		Quote(`__sr_`, u.index).Write(`.*)), '[]') AS "json" FROM (`)
	if err := my.buildStatsCore(ctx, u); err != nil {
		return err
	}
	ctx.Write(`) AS `).Quote(`__sr_`, u.index)
	return nil
}

// buildStatsCore 聚合核心：投影 + WHERE + GROUP BY + LIMIT
func (my *Dialect) buildStatsCore(ctx *compiler.Context, u *unit) error {
	sc := scope{class: u.class, qualifier: u.class.Table}
	ctx.MarkTable(u.class.Table)

	groups, err := my.groupColumns(sc, u.args)
	if err != nil {
		return err
	}

	// 投影：选择驱动，逐字段生成聚合表达式
	column := func(name string) {
		ctx.Quote(u.class.Table).Write(`.`).Quote(sc.column(name))
	}
	written := 0
	comma := func() {
		if written > 0 {
			ctx.SpaceAfter(`,`)
		}
		written++
	}
	ctx.SpaceAfter(`SELECT`)
	for _, f := range fieldsOf(u.field.SelectionSet) {
		comma()
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
				ctx.Write(`'`, sub.Alias, `', `)
				if strings.Contains(function, "%s") { // COUNT(DISTINCT col)
					ctx.Write(`COUNT(DISTINCT `)
					column(f.Name)
					ctx.Write(`)`)
				} else {
					ctx.Write(function, `(`)
					column(f.Name)
					ctx.Write(`)`)
				}
			}
			ctx.Write(`) AS `).Quote(f.Alias)
		}
	}
	if written == 0 {
		return fmt.Errorf("统计查询 %s 选择集为空", u.field.Name)
	}

	ctx.Space(`FROM`).Write(u.class.Table)
	if err = my.buildWhere(ctx, sc, u.args, nil); err != nil {
		return err
	}
	if len(groups) > 0 {
		ctx.Space(`GROUP BY`)
		for i, name := range groups {
			if i > 0 {
				ctx.Write(`, `)
			}
			column(name)
		}
	}
	return my.buildLimit(ctx, u)
}

// groupColumns 解析groupBy参数为字段名列表（须为实体的真实列）
func (my *Dialect) groupColumns(sc scope, args ast.ArgumentList) ([]string, error) {
	arg := args.ForName(protocol.GROUP_BY)
	if arg == nil || arg.Value == nil {
		return nil, nil
	}

	values := arg.Value.Children
	if len(values) == 0 && arg.Value.Raw != "" { // 单值写法 groupBy: "name"
		values = []*ast.ChildValue{{Value: arg.Value}}
	}
	groups := make([]string, 0, len(values))
	for _, child := range values {
		name := child.Value.Raw
		field, ok := sc.class.Fields[name]
		if !ok || field.Column == "" {
			return nil, fmt.Errorf("groupBy包含无效字段: %s", name)
		}
		groups = append(groups, name)
	}
	return groups, nil
}
