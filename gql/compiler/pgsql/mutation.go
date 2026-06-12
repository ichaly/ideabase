// Package pgsql 变更语句编译模块
// 遵循 doc/pgsql-template-design.md 的变更CTE模板：
// WITH "表名" AS (INSERT/UPDATE/DELETE ... RETURNING *) SELECT JSONB_BUILD_OBJECT(...)
// CTE名与表名一致，读回单元的基础查询自然命中CTE，与查询编译复用同一套机制
package pgsql

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2/ast"
)

// mutation 单个变更操作：op + 目标实体 + 可选读回单元（delete返回计数无单元）
type mutation struct {
	field *ast.Field
	class *protocol.Class
	op    string
	unit  *unit
}

// BuildMutation 构建变更语句
func (my *Dialect) BuildMutation(ctx *compiler.Context, set ast.SelectionSet) error {
	fields := fieldsOf(set)
	if len(fields) == 0 {
		return fmt.Errorf("变更选择集为空")
	}

	muts := make([]*mutation, 0, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		op, className := parseMutation(field.Name)
		class, ok := ctx.GetClass(className)
		if op == "" || !ok {
			return fmt.Errorf("不支持的变更字段: %s", field.Name)
		}
		// CTE名与表名一致，同一操作内不能重复变更同一张表
		if seen[class.Table] {
			return fmt.Errorf("同一操作中不能多次变更表: %s", class.Table)
		}
		seen[class.Table] = true
		ctx.MarkTable(class.Table)

		m := &mutation{field: field, class: class, op: op}
		if op != protocol.DELETE {
			m.unit = &unit{field: field, class: class, single: true, index: ctx.NextIndex()}
		}
		muts = append(muts, m)
	}

	// 变更CTE
	ctx.Write(`WITH `)
	for i, m := range muts {
		if i > 0 {
			ctx.SpaceAfter(`,`)
		}
		ctx.Quote(m.class.Table).Write(` AS (`)
		var err error
		switch m.op {
		case protocol.CREATE:
			err = my.buildInsert(ctx, m.class, m.field)
		case protocol.UPDATE:
			err = my.buildUpdate(ctx, m.class, m.field)
		case protocol.DELETE:
			err = my.buildDelete(ctx, m.class, m.field)
		}
		if err != nil {
			return err
		}
		ctx.Write(` RETURNING *)`)
	}

	// 统一__root读回：create/update经LATERAL读回实体，delete内联计数
	ctx.Space(`SELECT JSONB_BUILD_OBJECT(`)
	for i, m := range muts {
		if i > 0 {
			ctx.Write(`, `)
		}
		ctx.Write(`'`, m.field.Alias, `', `)
		if m.unit == nil {
			ctx.Write(`(SELECT COUNT(*) FROM `).Quote(m.class.Table).Write(`)`)
		} else {
			ctx.Quote(`__sj_`, m.unit.index).Write(`."json"`)
		}
	}
	ctx.Write(`) AS "__root" FROM (SELECT TRUE) AS "__root_x"`)

	for _, m := range muts {
		if m.unit != nil {
			if err := my.buildUnit(ctx, m.unit); err != nil {
				return err
			}
		}
	}
	return nil
}

// parseMutation 解析变更字段名：createUser -> (create, User)
func parseMutation(name string) (string, string) {
	for _, op := range []string{protocol.CREATE, protocol.UPDATE, protocol.DELETE} {
		if strings.HasPrefix(name, op) {
			return op, strings.TrimPrefix(name, op)
		}
	}
	return "", ""
}

// buildInsert 构建INSERT语句
func (my *Dialect) buildInsert(ctx *compiler.Context, class *protocol.Class, field *ast.Field) error {
	entries, err := my.inputEntries(ctx, class, field)
	if err != nil {
		return err
	}

	ctx.Write(`INSERT INTO `).Write(class.Table).Write(` (`)
	for i, e := range entries {
		if i > 0 {
			ctx.Write(`, `)
		}
		ctx.Quote(e.column)
	}
	ctx.Write(`) VALUES (`)
	for i, e := range entries {
		if i > 0 {
			ctx.Write(`, `)
		}
		if err := e.write(ctx); err != nil {
			return err
		}
	}
	ctx.Write(`)`)
	return nil
}

// buildUpdate 构建UPDATE语句，必须携带id或where条件
func (my *Dialect) buildUpdate(ctx *compiler.Context, class *protocol.Class, field *ast.Field) error {
	entries, err := my.inputEntries(ctx, class, field)
	if err != nil {
		return err
	}

	ctx.Write(`UPDATE `).Write(class.Table).Write(` SET `)
	for i, e := range entries {
		if i > 0 {
			ctx.Write(`, `)
		}
		ctx.Quote(e.column).Write(` = `)
		if err := e.write(ctx); err != nil {
			return err
		}
	}
	return my.buildMutationWhere(ctx, class, field)
}

// buildDelete 构建DELETE语句，必须携带id或where条件
func (my *Dialect) buildDelete(ctx *compiler.Context, class *protocol.Class, field *ast.Field) error {
	ctx.Write(`DELETE FROM `).Write(class.Table)
	return my.buildMutationWhere(ctx, class, field)
}

// buildMutationWhere 变更条件：强制要求条件，杜绝误操作全表
func (my *Dialect) buildMutationWhere(ctx *compiler.Context, class *protocol.Class, field *ast.Field) error {
	sc := scope{class: class, qualifier: class.Table}
	if len(my.collectConditions(field.Arguments)) == 0 {
		return fmt.Errorf("%s需要id或where条件", field.Name)
	}
	return my.buildWhere(ctx, sc, field.Arguments, nil)
}

// inputEntry input中的一列：列名 + 参数写入闭包
type inputEntry struct {
	column string
	write  func(*compiler.Context) error
}

// inputEntries 解析input参数为列与值。支持两种形态：
// 字面量对象逐字段编译；整体变量则读取运行期变量内容（编译产物不可缓存）
func (my *Dialect) inputEntries(ctx *compiler.Context, class *protocol.Class, field *ast.Field) ([]*inputEntry, error) {
	arg := field.Arguments.ForName(protocol.INPUT)
	if arg == nil || arg.Value == nil {
		return nil, fmt.Errorf("%s缺少input参数", field.Name)
	}

	column := func(name string) (string, error) {
		f, ok := class.Fields[name]
		if !ok || f.Column == "" {
			return "", fmt.Errorf("input包含未知或不可写字段: %s（暂不支持嵌套写入）", name)
		}
		return f.Column, nil
	}

	var entries []*inputEntry
	if arg.Value.Kind == ast.Variable {
		// 整体input变量：列集合取决于变量内容
		ctx.MarkVolatile()
		raw, _ := ctx.Variable(arg.Value.Raw)
		object, ok := raw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("input变量 %s 缺失或不是对象", arg.Value.Raw)
		}
		names := make([]string, 0, len(object))
		for name := range object {
			names = append(names, name)
		}
		sort.Strings(names) // 排序保证SQL确定性
		for _, name := range names {
			col, err := column(name)
			if err != nil {
				return nil, err
			}
			value := normalizeArg(object[name])
			entries = append(entries, &inputEntry{column: col, write: func(c *compiler.Context) error {
				c.Write(my.Placeholder(c.AddParam(value)))
				return nil
			}})
		}
		return entries, nil
	}

	for _, child := range arg.Value.Children {
		col, err := column(child.Name)
		if err != nil {
			return nil, err
		}
		value := child.Value
		entries = append(entries, &inputEntry{column: col, write: func(c *compiler.Context) error {
			return my.buildParam(c, value)
		}})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%s的input不能为空", field.Name)
	}
	return entries, nil
}

// normalizeArg 复杂值（对象/数组）序列化为JSON字符串，适配json/jsonb列
func normalizeArg(value interface{}) interface{} {
	switch value.(type) {
	case map[string]interface{}, []interface{}:
		data, err := json.Marshal(value)
		if err != nil {
			return value
		}
		return string(data)
	}
	return value
}
