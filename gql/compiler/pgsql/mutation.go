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

// relationOp 输入中的关系操作：原子挂接/解除目标实体
// 主键列表存为参数写入器：字面量/变量元素统一经槽位参数化
type relationOp struct {
	rel        *protocol.Relation
	connect    []paramWriter
	disconnect []paramWriter
}

// paramWriter 把一个参数值写入SQL（占位符+槽位）
type paramWriter func(*compiler.Context) error

// BuildMutation 构建变更语句
func (my *Dialect) BuildMutation(ctx *compiler.Context, set ast.SelectionSet) error {
	fields := fieldsOf(set)
	if len(fields) == 0 {
		return fmt.Errorf("变更选择集为空")
	}

	muts := make([]*mutation, 0, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		if field.Name == typename {
			continue // 元字段在根对象输出字面量
		}
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
	if len(muts) == 0 {
		return fmt.Errorf("变更选择集为空")
	}

	// 变更CTE + 关系操作CTE（同一语句原子完成）
	ctx.Write(`WITH `)
	for i, m := range muts {
		if i > 0 {
			ctx.SpaceAfter(`,`)
		}
		ctx.Quote(m.class.Table).Write(` AS (`)
		var ops []relationOp
		var err error
		switch m.op {
		case protocol.CREATE:
			ops, err = my.buildInsert(ctx, m.class, m.field)
		case protocol.UPDATE:
			ops, err = my.buildUpdate(ctx, m.class, m.field)
		case protocol.DELETE:
			err = my.buildDelete(ctx, m.class, m.field)
		}
		if err != nil {
			return err
		}
		ctx.Write(`)`)
		if err = my.buildRelationOps(ctx, m.class, ops); err != nil {
			return err
		}
	}

	// 统一__root读回：create/update经LATERAL读回实体，delete内联计数，__typename字面量
	ctx.Space(`SELECT JSONB_BUILD_OBJECT(`)
	pending := muts
	for i, field := range fields {
		if i > 0 {
			ctx.Write(`, `)
		}
		if field.Name == typename {
			ctx.Write(`'`, field.Alias, `', 'Mutation'`)
			continue
		}
		m := pending[0]
		pending = pending[1:]
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
func (my *Dialect) buildInsert(ctx *compiler.Context, class *protocol.Class, field *ast.Field) ([]relationOp, error) {
	entries, ops, err := my.inputEntries(ctx, class, field)
	if err != nil {
		return nil, err
	}
	for _, op := range ops {
		if len(op.disconnect) > 0 {
			return nil, fmt.Errorf("创建时不支持disconnect（无可解除的既有关系）")
		}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%s的input不能为空", field.Name)
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
			return nil, err
		}
	}
	ctx.Write(`) RETURNING *`)
	return ops, nil
}

// buildUpdate 构建UPDATE语句，必须携带id或where条件
// 仅含关系操作时主CTE退化为行锚点SELECT（无列可SET）
func (my *Dialect) buildUpdate(ctx *compiler.Context, class *protocol.Class, field *ast.Field) ([]relationOp, error) {
	entries, ops, err := my.inputEntries(ctx, class, field)
	if err != nil {
		return nil, err
	}
	if len(ops) > 0 && field.Arguments.ForName(protocol.ID) == nil {
		return nil, fmt.Errorf("携带关系操作的更新必须用id定位单行")
	}

	if len(entries) == 0 {
		if len(ops) == 0 {
			return nil, fmt.Errorf("%s的input不能为空", field.Name)
		}
		ctx.Write(`SELECT * FROM `).Write(class.Table)
		return ops, my.buildMutationWhere(ctx, class, field)
	}

	ctx.Write(`UPDATE `).Write(class.Table).Write(` SET `)
	for i, e := range entries {
		if i > 0 {
			ctx.Write(`, `)
		}
		ctx.Quote(e.column).Write(` = `)
		if err := e.write(ctx); err != nil {
			return nil, err
		}
	}
	if err = my.buildMutationWhere(ctx, class, field); err != nil {
		return nil, err
	}
	ctx.Write(` RETURNING *`)
	return ops, nil
}

// buildDelete 构建DELETE语句，必须携带id或where条件
func (my *Dialect) buildDelete(ctx *compiler.Context, class *protocol.Class, field *ast.Field) error {
	ctx.Write(`DELETE FROM `).Write(class.Table)
	if err := my.buildMutationWhere(ctx, class, field); err != nil {
		return err
	}
	ctx.Write(` RETURNING *`)
	return nil
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

// relationField 判断input子项是否为列表关系虚拟字段（关系操作载体）
func relationField(class *protocol.Class, name string) *protocol.Field {
	if f, ok := class.Fields[name]; ok && f.Column == "" && f.Relation != nil && f.IsList {
		return f
	}
	return nil
}

// inputEntries 解析input参数为标量列与关系操作。支持两种形态：
// 字面量对象逐字段编译；整体变量则读取运行期变量内容（编译产物不可缓存）
func (my *Dialect) inputEntries(ctx *compiler.Context, class *protocol.Class, field *ast.Field) ([]*inputEntry, []relationOp, error) {
	arg := field.Arguments.ForName(protocol.INPUT)
	if arg == nil || arg.Value == nil {
		return nil, nil, fmt.Errorf("%s缺少input参数", field.Name)
	}

	column := func(name string) (string, error) {
		f, ok := class.Fields[name]
		if !ok || f.Column == "" {
			return "", fmt.Errorf("input包含未知或不可写字段: %s", name)
		}
		return f.Column, nil
	}

	var entries []*inputEntry
	var ops []relationOp
	if arg.Value.Kind == ast.Variable {
		// 整体input变量：内容决定SQL形态
		ctx.MarkVolatile()
		raw, _ := ctx.Variable(arg.Value.Raw)
		object, ok := raw.(map[string]interface{})
		if !ok {
			return nil, nil, fmt.Errorf("input变量 %s 缺失或不是对象", arg.Value.Raw)
		}
		names := make([]string, 0, len(object))
		for name := range object {
			names = append(names, name)
		}
		sort.Strings(names) // 排序保证SQL确定性
		for _, name := range names {
			if rel := relationField(class, name); rel != nil {
				op, err := my.rawRelationOp(rel, object[name])
				if err != nil {
					return nil, nil, err
				}
				ops = append(ops, op)
				continue
			}
			col, err := column(name)
			if err != nil {
				return nil, nil, err
			}
			value := normalizeArg(object[name])
			entries = append(entries, &inputEntry{column: col, write: func(c *compiler.Context) error {
				c.Write(my.Placeholder(c.AddParam(value)))
				return nil
			}})
		}
		return entries, ops, nil
	}

	for _, child := range arg.Value.Children {
		if rel := relationField(class, child.Name); rel != nil {
			op, err := my.literalRelationOp(ctx, rel, child.Value)
			if err != nil {
				return nil, nil, err
			}
			ops = append(ops, op)
			continue
		}
		col, err := column(child.Name)
		if err != nil {
			return nil, nil, err
		}
		value := child.Value
		entries = append(entries, &inputEntry{column: col, write: func(c *compiler.Context) error {
			return my.buildParam(c, value)
		}})
	}
	return entries, ops, nil
}

// literalRelationOp 解析字面量RelationInput{connect, disconnect}
func (my *Dialect) literalRelationOp(ctx *compiler.Context, field *protocol.Field, value *ast.Value) (relationOp, error) {
	op := relationOp{rel: field.Relation}
	if value == nil {
		return op, nil
	}
	for _, child := range value.Children {
		ids, err := my.idWriters(ctx, child.Value)
		if err != nil {
			return op, err
		}
		switch child.Name {
		case protocol.CONNECT:
			op.connect = ids
		case protocol.DISCONNECT:
			op.disconnect = ids
		}
	}
	return op, nil
}

// idWriters 主键列表转参数写入器：列表元素可为字面量或变量（经槽位参数化保持计划可缓存）；
// 整列表变量则按内容展开（volatile）
func (my *Dialect) idWriters(ctx *compiler.Context, value *ast.Value) ([]paramWriter, error) {
	if value == nil {
		return nil, nil
	}
	if value.Kind == ast.Variable {
		ctx.MarkVolatile()
		raw, _ := ctx.Variable(value.Raw)
		list, ok := raw.([]interface{})
		if !ok {
			return nil, fmt.Errorf("关系操作变量 %s 缺失或不是列表", value.Raw)
		}
		return rawWriters(my, list), nil
	}
	writers := make([]paramWriter, 0, len(value.Children))
	for _, child := range value.Children {
		id := child.Value
		writers = append(writers, func(c *compiler.Context) error {
			return my.buildParam(c, id)
		})
	}
	return writers, nil
}

// rawWriters 运行期值转参数写入器
func rawWriters(my *Dialect, values []interface{}) []paramWriter {
	writers := make([]paramWriter, 0, len(values))
	for _, value := range values {
		id := value
		writers = append(writers, func(c *compiler.Context) error {
			c.Write(my.Placeholder(c.AddParam(id)))
			return nil
		})
	}
	return writers
}

// rawRelationOp 解析整体变量input中的关系操作对象
func (my *Dialect) rawRelationOp(field *protocol.Field, raw interface{}) (relationOp, error) {
	op := relationOp{rel: field.Relation}
	object, ok := raw.(map[string]interface{})
	if !ok {
		return op, fmt.Errorf("关系操作 %s 必须是对象", field.Name)
	}
	pick := func(name string) ([]paramWriter, error) {
		value, exists := object[name]
		if !exists || value == nil {
			return nil, nil
		}
		list, ok := value.([]interface{})
		if !ok {
			return nil, fmt.Errorf("关系操作 %s.%s 必须是列表", field.Name, name)
		}
		return rawWriters(my, list), nil
	}
	var err error
	if op.connect, err = pick(protocol.CONNECT); err != nil {
		return op, err
	}
	op.disconnect, err = pick(protocol.DISCONNECT)
	return op, err
}

// buildRelationOps 关系操作CTE：一对多更新外键，多对多插删中间表，与主变更同语句原子
func (my *Dialect) buildRelationOps(ctx *compiler.Context, class *protocol.Class, ops []relationOp) error {
	// 主CTE单行锚点：(SELECT 源列 FROM "表名CTE")
	anchor := func(rel *protocol.Relation) {
		sourceCol := scope{class: class}.column(rel.SourceFiled)
		ctx.Write(`(SELECT `).Quote(sourceCol).Write(` FROM `).Quote(class.Table).Write(`)`)
	}
	params := func(writers []paramWriter) error {
		for i, write := range writers {
			if i > 0 {
				ctx.Write(`, `)
			}
			if err := write(ctx); err != nil {
				return err
			}
		}
		return nil
	}

	for _, op := range ops {
		target, ok := ctx.GetClass(op.rel.TargetClass)
		if !ok {
			return fmt.Errorf("关系目标类不存在: %s", op.rel.TargetClass)
		}
		sc := scope{class: target}

		if through := op.rel.Through; through != nil {
			// 多对多：中间表插入/删除
			ctx.MarkTable(through.TableName)
			if len(op.connect) > 0 {
				// INSERT VALUES 上下文中参数类型由目标列推断
				ctx.Write(`, `).Quote(`__c_`, ctx.NextIndex()).
					Write(` AS (INSERT INTO `, through.TableName, ` (`).
					Quote(through.SourceKey).Write(`, `).Quote(through.TargetKey).
					Write(`) VALUES `)
				for i, write := range op.connect {
					if i > 0 {
						ctx.Write(`, `)
					}
					ctx.Write(`(`)
					anchor(op.rel)
					ctx.Write(`, `)
					if err := write(ctx); err != nil {
						return err
					}
					ctx.Write(`)`)
				}
				ctx.Write(`)`)
			}
			if len(op.disconnect) > 0 {
				ctx.Write(`, `).Quote(`__c_`, ctx.NextIndex()).
					Write(` AS (DELETE FROM `, through.TableName, ` WHERE `).
					Quote(through.SourceKey).Write(` = `)
				anchor(op.rel)
				ctx.Write(` AND `).Quote(through.TargetKey).Write(` IN (`)
				if err := params(op.disconnect); err != nil {
					return err
				}
				ctx.Write(`))`)
			}
			continue
		}

		// 一对多：更新目标表外键
		if len(target.PrimaryKeys) != 1 {
			return fmt.Errorf("关系操作要求目标实体 %s 有单一主键", target.Name)
		}
		ctx.MarkTable(target.Table)
		fk := sc.column(op.rel.TargetFiled)
		pk := sc.column(target.PrimaryKeys[0])
		if len(op.connect) > 0 {
			ctx.Write(`, `).Quote(`__c_`, ctx.NextIndex()).
				Write(` AS (UPDATE `, target.Table, ` SET `).Quote(fk).Write(` = `)
			anchor(op.rel)
			ctx.Write(` WHERE `).Quote(pk).Write(` IN (`)
			if err := params(op.connect); err != nil {
				return err
			}
			ctx.Write(`))`)
		}
		if len(op.disconnect) > 0 {
			ctx.Write(`, `).Quote(`__c_`, ctx.NextIndex()).
				Write(` AS (UPDATE `, target.Table, ` SET `).Quote(fk).Write(` = NULL WHERE `).
				Quote(fk).Write(` = `)
			anchor(op.rel)
			ctx.Write(` AND `).Quote(pk).Write(` IN (`)
			if err := params(op.disconnect); err != nil {
				return err
			}
			ctx.Write(`))`)
		}
	}
	return nil
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
