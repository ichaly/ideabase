// Package pgsql 游标分页编译模块
// keyset语义：排序键自动追加主键兜底保证全序；cursor=base64(JSON数组：边界行键值)，
// SQL行级生成；取N+1行探测hasNext，wrapper按行号FILTER聚合items与pageInfo
package pgsql

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2/ast"
)

// pageKey 游标排序键
type pageKey struct {
	column string
	desc   bool
}

// pager 游标分页编译参数
type pager struct {
	limit    int        // first/last 的N（字面量；变量时为0）
	countVar string     // first/last 为变量时的变量名（非空=变量）
	last     bool       // 向后翻页（last/before）
	keys     []pageKey  // 排序键（含主键兜底）
	cursor   *ast.Value // after/before 的游标值，可为nil（首页）
}

// writeSize 写页大小N：字面量内联（可选+plus），变量注册参数槽位（$k [+ plus]）
// 变量化使 first:$n 的查询缓存一份计划即可适配任意页大小
func (p *pager) writeSize(ctx *compiler.Context, d *Dialect, plus int) {
	if p.countVar != "" {
		ctx.Write(d.Placeholder(ctx.AddVariable(p.countVar)))
		if plus != 0 {
			ctx.Write(" + ", plus)
		}
		return
	}
	ctx.Write(p.limit + plus)
}

// newPager 解析游标分页参数并校验约束；非游标模式返回nil
func newPager(sc scope, args ast.ArgumentList) (*pager, error) {
	firstHas, first, firstVar, err := parseCount(args, protocol.FIRST)
	if err != nil {
		return nil, err
	}
	lastHas, last, lastVar, err := parseCount(args, protocol.LAST)
	if err != nil {
		return nil, err
	}
	after, before := args.ForName(protocol.AFTER), args.ForName(protocol.BEFORE)

	if !firstHas && !lastHas {
		if after != nil || before != nil {
			return nil, fmt.Errorf("after/before必须配合first/last使用")
		}
		return nil, nil
	}
	switch {
	case firstHas && lastHas:
		return nil, fmt.Errorf("first与last不能同时使用")
	case args.ForName(protocol.LIMIT) != nil || args.ForName(protocol.OFFSET) != nil:
		return nil, fmt.Errorf("游标分页与limit/offset不能同时使用")
	case firstHas && before != nil:
		return nil, fmt.Errorf("before必须配合last使用")
	case lastHas && after != nil:
		return nil, fmt.Errorf("after必须配合first使用")
	}

	my := &pager{}
	if lastHas {
		my.limit, my.countVar, my.last = last, lastVar, true
		if before != nil {
			my.cursor = before.Value
		}
	} else {
		my.limit, my.countVar = first, firstVar
		if after != nil {
			my.cursor = after.Value
		}
	}

	// 排序键 = 用户sort + 主键兜底（保证全序与游标确定性）
	tail := make(map[string]bool)
	for _, child := range sortEntries(args) {
		direction := ""
		if child.Value != nil {
			direction = directions[strings.ToUpper(child.Value.Raw)]
		}
		column := sc.column(child.Name)
		my.keys = append(my.keys, pageKey{column: column, desc: strings.HasPrefix(direction, "DESC")})
		tail[column] = true
	}
	for _, pk := range sc.class.PrimaryKeys {
		if column := sc.column(pk); !tail[column] {
			my.keys = append(my.keys, pageKey{column: column})
		}
	}
	if len(my.keys) == 0 {
		return nil, fmt.Errorf("游标分页需要实体定义主键")
	}
	return my, nil
}

// parseCount 解析first/last：present标识是否提供，变量返回varName，字面量返回正整数。
// 变量页大小执行期填入（须为正整数），同一查询文本缓存一份计划
func parseCount(args ast.ArgumentList, name string) (present bool, count int, varName string, err error) {
	arg := args.ForName(name)
	if arg == nil || arg.Value == nil {
		return false, 0, "", nil
	}
	if arg.Value.Kind == ast.Variable {
		return true, 0, arg.Value.Raw, nil
	}
	count, err = strconv.Atoi(arg.Value.Raw)
	if err != nil || count <= 0 {
		return false, 0, "", fmt.Errorf("%s必须是正整数或变量", name)
	}
	return true, count, "", nil
}

// order 第i个键的实际方向（向后翻页时反转取行，items聚合时再还原显示顺序）
func (my *pager) order(i int) string {
	desc := my.keys[i].desc
	if my.last {
		desc = !desc
	}
	if desc {
		return "DESC"
	}
	return "ASC"
}

// buildKeyset 边界条件展开：k1>v1 OR (k1=v1 AND k2>v2) ...（方向按键序与翻页方向决定）
// 变量游标运行期可能为null（首页），统一为 ($1 IS NULL OR ...) 形态保住计划缓存
func (my *Dialect) buildKeyset(ctx *compiler.Context, sc scope, p *pager) error {
	values, err := my.cursorParams(ctx, p)
	if err != nil {
		return err
	}

	column := func(i int) {
		ctx.Column(sc.qualifier, p.keys[i].column)
	}
	operator := func(i int) string {
		if p.order(i) == "DESC" {
			return ` < `
		}
		return ` > `
	}

	ctx.Write(`(`)
	if p.cursor.Kind == ast.Variable {
		// 游标原始串判空（恒为text无类型歧义），null游标=首页恒真
		ctx.Write(my.Placeholder(ctx.AddVariable(p.cursor.Raw)), `::text IS NULL OR `)
	}
	for i := range p.keys {
		if i > 0 {
			ctx.Write(` OR (`)
			for j := 0; j < i; j++ {
				if j > 0 {
					ctx.Write(` AND `)
				}
				column(j)
				ctx.Write(` = `, values[j])
			}
			ctx.Write(` AND `)
		}
		column(i)
		ctx.Write(operator(i), values[i])
		if i > 0 {
			ctx.Write(`)`)
		}
	}
	ctx.Write(`)`)
	return nil
}

// cursorParams 游标键值参数化：字面量编译期解码，变量执行期按下标抽取
func (my *Dialect) cursorParams(ctx *compiler.Context, p *pager) ([]string, error) {
	placeholders := make([]string, len(p.keys))
	if p.cursor.Kind == ast.Variable {
		for i := range p.keys {
			placeholders[i] = my.Placeholder(ctx.AddCursor(p.cursor.Raw, i))
		}
		return placeholders, nil
	}

	keys, err := compiler.DecodeCursor(p.cursor.Raw)
	if err != nil {
		return nil, err
	}
	if len(keys) != len(p.keys) {
		return nil, fmt.Errorf("游标键数量与排序键不匹配（游标须来自相同排序的查询）")
	}
	for i, value := range keys {
		placeholders[i] = my.Placeholder(ctx.AddParam(value))
	}
	return placeholders, nil
}
