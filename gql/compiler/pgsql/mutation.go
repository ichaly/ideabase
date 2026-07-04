// Package pgsql 变更语句编译模块
// 遵循 doc/pgsql-template-design.md 的变更CTE模板：
// WITH "表名" AS (INSERT/UPDATE/DELETE ... RETURNING *) [, 关系操作CTE...] SELECT JSONB_BUILD_OBJECT(...)
// CTE名与表名一致，读回单元的基础查询自然命中CTE，与查询编译复用同一套机制。
// 输入统一为行模型：单条/批量/upsert/嵌套创建共用解析与多行VALUES生成。
package pgsql

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/utl"
	"github.com/jinzhu/inflection"
	"github.com/vektah/gqlparser/v2/ast"
)

// mutation 单个变更操作：op + 目标实体 + 可选读回单元（delete返回计数无单元）
type mutation struct {
	field *ast.Field
	class *protocol.Class
	op    string
	bulk  bool // 复数字段：批量语义，读回为数组
	unit  *unit
}

// paramWriter 把一个参数值写入SQL（占位符+槽位）
type paramWriter func(*compiler.Context) error

// inputRow 一行输入：列名（首现顺序）与各列的参数写入器
type inputRow struct {
	columns []string
	values  map[string]paramWriter
	ops     []relationOp
}

// relationOp 输入中的关系操作：挂接/解除既有行，或内联创建新行
type relationOp struct {
	rel        *protocol.Relation
	connect    []paramWriter
	disconnect []paramWriter
	create     []inputRow
}

// BuildMutation 构建变更语句
func (my *Dialect) BuildMutation(ctx *compiler.Context, set ast.SelectionSet) error {
	fields := compiler.FieldsOf(set)
	if len(fields) == 0 {
		return fmt.Errorf("变更选择集为空")
	}

	muts := make([]*mutation, 0, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		if field.Name == typename {
			continue // 元字段在根对象输出字面量
		}
		m, err := parseMutation(ctx, field)
		if err != nil {
			return err
		}
		// CTE名与表名一致，同一操作内不能重复变更同一张表
		if seen[m.class.Table] {
			return fmt.Errorf("同一操作中不能多次变更表: %s", m.class.Table)
		}
		seen[m.class.Table] = true
		ctx.MarkTable(m.class.Table)

		if m.op != protocol.DELETE {
			kind := shapeSingle
			if m.bulk {
				kind = shapeList
			}
			m.unit = &unit{field: field, class: m.class, shape: kind, index: ctx.NextIndex(), readback: true}
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
			ops, err = my.buildInsert(ctx, m)
		case protocol.UPSERT:
			err = my.buildUpsert(ctx, m)
		case protocol.UPDATE:
			ops, err = my.buildUpdate(ctx, m)
		case protocol.DELETE:
			err = my.buildDelete(ctx, m)
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
	byField := make(map[*ast.Field]*mutation, len(muts))
	for _, m := range muts {
		byField[m.field] = m
	}
	ctx.Space(`SELECT JSONB_BUILD_OBJECT(`)
	for i, field := range fields {
		if i > 0 {
			ctx.Write(`, `)
		}
		m, ok := byField[field]
		if !ok {
			ctx.Write(`'`, field.Alias, `', 'Mutation'`)
			continue
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

// parseMutation 解析变更字段：createUser单条 / createUsers批量 / upsertUsers / updateX / deleteX
func parseMutation(ctx *compiler.Context, field *ast.Field) (*mutation, error) {
	for _, op := range []string{protocol.CREATE, protocol.UPSERT, protocol.UPDATE, protocol.DELETE} {
		if !strings.HasPrefix(field.Name, op) {
			continue
		}
		name := strings.TrimPrefix(field.Name, op)
		if class, ok := ctx.GetClass(name); ok {
			return &mutation{field: field, class: class, op: op}, nil
		}
		// 复数字段=批量语义
		if class, ok := ctx.GetClass(inflection.Singular(name)); ok {
			return &mutation{field: field, class: class, op: op, bulk: true}, nil
		}
	}
	return nil, fmt.Errorf("不支持的变更字段: %s", field.Name)
}

// buildInsert 构建INSERT：单条或批量多行VALUES；批量不支持关系操作
func (my *Dialect) buildInsert(ctx *compiler.Context, m *mutation) ([]relationOp, error) {
	rows, err := my.inputRows(ctx, m.class, m.field)
	if err != nil {
		return nil, err
	}
	if !m.bulk && len(rows) > 1 {
		return nil, fmt.Errorf("%s是单条创建，批量请用%s%s", m.field.Name, m.op, inflection.Plural(m.class.Name))
	}
	var ops []relationOp
	for _, row := range rows {
		for _, op := range row.ops {
			if len(op.disconnect) > 0 {
				return nil, fmt.Errorf("创建时不支持disconnect（无可解除的既有关系）")
			}
		}
		if m.bulk && len(row.ops) > 0 {
			return nil, fmt.Errorf("批量创建不支持关系操作（无法确定挂接到哪一行）")
		}
		ops = append(ops, row.ops...)
	}

	my.applyScope(m.class, rows) // 强制作用域列=上下文值（防越租户创建）
	if _, err = my.writeInsertValues(ctx, m.class.Table, rows); err != nil {
		return nil, err
	}
	ctx.Write(` RETURNING *`)
	return ops, nil
}

// buildUpsert 构建INSERT ... ON CONFLICT DO UPDATE（批量语义）
func (my *Dialect) buildUpsert(ctx *compiler.Context, m *mutation) error {
	rows, err := my.inputRows(ctx, m.class, m.field)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if len(row.ops) > 0 {
			return fmt.Errorf("upsert不支持关系操作")
		}
	}

	// 冲突列：on参数（字段名），缺省主键
	sc := scope{class: m.class}
	names, err := fieldNames(sc, m.field.Arguments, protocol.ON)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		names = m.class.PrimaryKeys
	}
	conflicts := columnsOf(sc, names)
	if len(conflicts) == 0 {
		return fmt.Errorf("upsert需要冲突列（on参数或实体主键）")
	}

	my.applyScope(m.class, rows) // 强制作用域列=上下文值（防越租户创建）
	columns, err := my.writeInsertValues(ctx, m.class.Table, rows)
	if err != nil {
		return err
	}

	ctx.Write(` ON CONFLICT (`)
	writeColumns(ctx, "", conflicts)
	ctx.Write(`) DO UPDATE SET `)
	// 冲突列与作用域列都不进SET：作用域列租户/属主归属不可变更
	skip := make(map[string]bool, len(conflicts)+len(m.class.Scope))
	for _, column := range conflicts {
		skip[column] = true
	}
	for _, rule := range m.class.Scope {
		skip[rule.Column] = true
	}
	written := 0
	for _, column := range columns {
		if skip[column] {
			continue
		}
		if written > 0 {
			ctx.Write(`, `)
		}
		written++
		ctx.Quote(column).Write(` = EXCLUDED.`).Quote(column)
	}
	if written == 0 {
		return fmt.Errorf("upsert没有可更新的非冲突列")
	}
	// 只更新属于当前作用域的冲突行：别租户/别属主的冲突行WHERE不满足→不更新（防主键劫持）
	for i, rule := range m.class.Scope {
		if i == 0 {
			ctx.Space(`WHERE`)
		} else {
			ctx.Space(`AND`)
		}
		my.scopeCondition(ctx, m.class.Table, rule)
	}
	ctx.Write(` RETURNING *`)
	return nil
}

// writeInsertValues 写INSERT INTO ... VALUES主体，返回并集列；
// 列集合取各行并集（首现顺序），行内缺失列填DEFAULT
func (my *Dialect) writeInsertValues(ctx *compiler.Context, table string, rows []inputRow) ([]string, error) {
	if len(rows) == 0 {
		return nil, fmt.Errorf("input不能为空")
	}

	columns := make([]string, 0, len(rows[0].columns))
	seen := make(map[string]bool)
	for _, row := range rows {
		for _, column := range row.columns {
			if !seen[column] {
				seen[column] = true
				columns = append(columns, column)
			}
		}
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("input不能为空")
	}

	ctx.Write(`INSERT INTO `)
	tableRef(ctx, table)
	ctx.Write(` (`)
	writeColumns(ctx, "", columns)
	ctx.Write(`) VALUES `)
	for i, row := range rows {
		if i > 0 {
			ctx.Write(`, `)
		}
		ctx.Write(`(`)
		for j, column := range columns {
			if j > 0 {
				ctx.Write(`, `)
			}
			write, ok := row.values[column]
			if !ok {
				ctx.Write(`DEFAULT`)
				continue
			}
			if err := write(ctx); err != nil {
				return nil, err
			}
		}
		ctx.Write(`)`)
	}
	return columns, nil
}

// buildUpdate 构建UPDATE语句，必须携带id或where条件
// 仅含关系操作时主CTE退化为行锚点SELECT（无列可SET）
func (my *Dialect) buildUpdate(ctx *compiler.Context, m *mutation) ([]relationOp, error) {
	if m.bulk {
		return nil, fmt.Errorf("更新没有批量字段形态，请用where条件: %s", m.field.Name)
	}
	rows, err := my.inputRows(ctx, m.class, m.field)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, fmt.Errorf("%s的input必须是单个对象", m.field.Name)
	}
	row := rows[0]
	if len(row.ops) > 0 && m.field.Arguments.ForName(protocol.ID) == nil {
		return nil, fmt.Errorf("携带关系操作的更新必须用id定位单行")
	}

	if len(row.columns) == 0 {
		if len(row.ops) == 0 {
			return nil, fmt.Errorf("%s的input不能为空", m.field.Name)
		}
		ctx.Write(`SELECT * FROM `)
		tableRef(ctx, m.class.Table)
		return row.ops, my.buildMutationWhere(ctx, m.class, m.field)
	}

	ctx.Write(`UPDATE `)
	tableRef(ctx, m.class.Table)
	ctx.Write(` SET `)
	for i, column := range row.columns {
		if i > 0 {
			ctx.Write(`, `)
		}
		ctx.Quote(column).Write(` = `)
		if err := row.values[column](ctx); err != nil {
			return nil, err
		}
	}
	if err = my.buildMutationWhere(ctx, m.class, m.field); err != nil {
		return nil, err
	}
	ctx.Write(` RETURNING *`)
	return row.ops, nil
}

// buildDelete 构建DELETE语句，必须携带id或where条件
func (my *Dialect) buildDelete(ctx *compiler.Context, m *mutation) error {
	ctx.Write(`DELETE FROM `)
	tableRef(ctx, m.class.Table)
	if err := my.buildMutationWhere(ctx, m.class, m.field); err != nil {
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
	// 行级作用域：update/delete 强制 AND 作用域，只能改本租户/属主的行
	return my.buildWhere(ctx, sc, field.Arguments, my.scopeConjuncts(ctx, sc.qualifier, class)...)
}

// applyScope 给每行填充作用域列=上下文值（客户端值已被writableColumn拒绝，此处直接填）
func (my *Dialect) applyScope(class *protocol.Class, rows []inputRow) {
	for _, rule := range class.Scope {
		for i := range rows {
			row := &rows[i]
			row.columns = append(row.columns, rule.Column)
			row.values[rule.Column] = func(c *compiler.Context) error {
				c.Write(my.Placeholder(c.AddContextSlot(rule.Context)))
				return nil
			}
		}
	}
}

// ---------- 输入行解析 ----------

// inputRows 解析input参数为行列表：单对象/对象列表/整体变量（map或列表）
func (my *Dialect) inputRows(ctx *compiler.Context, class *protocol.Class, field *ast.Field) ([]inputRow, error) {
	arg := field.Arguments.ForName(protocol.INPUT)
	if arg == nil || arg.Value == nil {
		return nil, fmt.Errorf("%s缺少input参数", field.Name)
	}
	return my.rowsOfValue(ctx, class, arg.Value)
}

// rowsOfValue AST值转行列表
func (my *Dialect) rowsOfValue(ctx *compiler.Context, class *protocol.Class, value *ast.Value) ([]inputRow, error) {
	switch value.Kind {
	case ast.Variable:
		ctx.MarkVolatile() // 行集合取决于变量内容
		raw, _ := ctx.Variable(value.Raw)
		return my.rowsOfRaw(ctx, class, raw, value.Raw)
	case ast.ListValue:
		rows := make([]inputRow, 0, len(value.Children))
		for _, child := range value.Children {
			row, err := my.literalRow(ctx, class, child.Value)
			if err != nil {
				return nil, err
			}
			rows = append(rows, row)
		}
		return rows, nil
	default:
		row, err := my.literalRow(ctx, class, value)
		if err != nil {
			return nil, err
		}
		return []inputRow{row}, nil
	}
}

// rowsOfRaw 运行期变量值转行列表
func (my *Dialect) rowsOfRaw(ctx *compiler.Context, class *protocol.Class, raw interface{}, name string) ([]inputRow, error) {
	switch value := raw.(type) {
	case map[string]interface{}:
		row, err := my.rawRow(ctx, class, value)
		if err != nil {
			return nil, err
		}
		return []inputRow{row}, nil
	case []interface{}:
		rows := make([]inputRow, 0, len(value))
		for _, item := range value {
			object, ok := item.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("input变量 %s 的元素必须是对象", name)
			}
			row, err := my.rawRow(ctx, class, object)
			if err != nil {
				return nil, err
			}
			rows = append(rows, row)
		}
		return rows, nil
	}
	return nil, fmt.Errorf("input变量 %s 缺失或不是对象/列表", name)
}

// literalRow 字面量对象转行
func (my *Dialect) literalRow(ctx *compiler.Context, class *protocol.Class, value *ast.Value) (inputRow, error) {
	row := inputRow{values: make(map[string]paramWriter)}
	if value == nil || len(value.Children) == 0 {
		return row, nil
	}
	for _, child := range value.Children {
		if rel := relationField(class, child.Name); rel != nil {
			op, err := my.literalRelationOp(ctx, rel, child.Value)
			if err != nil {
				return row, err
			}
			row.ops = append(row.ops, op)
			continue
		}
		column, err := writableColumn(class, child.Name)
		if err != nil {
			return row, err
		}
		row.columns = append(row.columns, column)
		row.values[column] = my.astWriter(child.Value)
	}
	return row, nil
}

// rawRow 运行期对象转行（键排序保证SQL确定性）
func (my *Dialect) rawRow(ctx *compiler.Context, class *protocol.Class, object map[string]interface{}) (inputRow, error) {
	row := inputRow{values: make(map[string]paramWriter)}
	for _, name := range utl.SortKeys(object) {
		if rel := relationField(class, name); rel != nil {
			op, err := my.rawRelationOp(ctx, rel, object[name])
			if err != nil {
				return row, err
			}
			row.ops = append(row.ops, op)
			continue
		}
		column, err := writableColumn(class, name)
		if err != nil {
			return row, err
		}
		row.columns = append(row.columns, column)
		row.values[column] = my.valWriter(normalizeArg(object[name]))
	}
	return row, nil
}

// writableColumn 校验并映射可写列
func writableColumn(class *protocol.Class, name string) (string, error) {
	f, ok := class.Fields[name]
	if !ok || f.Column == "" {
		return "", fmt.Errorf("input包含未知或不可写字段: %s", name)
	}
	// 作用域列由服务端强制填充，编译器层硬拒绝客户端写入（schema排除只挡字面量路径，
	// 整体变量input绕过schema校验，须在此堵住——否则可篡改租户/属主归属）
	for _, rule := range class.Scope {
		if f.Column == rule.Column {
			return "", fmt.Errorf("input不能包含作用域列（由服务端强制填充）: %s", name)
		}
	}
	return f.Column, nil
}

// relationField 判断input子项是否为列表关系虚拟字段（关系操作载体）
func relationField(class *protocol.Class, name string) *protocol.Field {
	if f, ok := class.Fields[name]; ok && f.Column == "" && f.Relation != nil && f.IsList {
		return f
	}
	return nil
}

// ---------- 关系操作 ----------

// literalRelationOp 解析字面量XxxRelationInput{connect, disconnect, create}
func (my *Dialect) literalRelationOp(ctx *compiler.Context, field *protocol.Field, value *ast.Value) (relationOp, error) {
	op := relationOp{rel: field.Relation}
	if value == nil {
		return op, nil
	}
	for _, child := range value.Children {
		switch child.Name {
		case protocol.CONNECT, protocol.DISCONNECT:
			ids, err := my.idWriters(ctx, child.Value)
			if err != nil {
				return op, err
			}
			if child.Name == protocol.CONNECT {
				op.connect = ids
			} else {
				op.disconnect = ids
			}
		case protocol.CREATE:
			target, ok := ctx.GetClass(field.Relation.TargetClass)
			if !ok {
				return op, fmt.Errorf("关系目标类不存在: %s", field.Relation.TargetClass)
			}
			rows, err := my.rowsOfValue(ctx, target, child.Value)
			if err != nil {
				return op, err
			}
			op.create = rows
		}
	}
	return op, nil
}

// rawRelationOp 解析整体变量input中的关系操作对象
func (my *Dialect) rawRelationOp(ctx *compiler.Context, field *protocol.Field, raw interface{}) (relationOp, error) {
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
	if op.disconnect, err = pick(protocol.DISCONNECT); err != nil {
		return op, err
	}
	if raw, exists := object[protocol.CREATE]; exists && raw != nil {
		target, ok := ctx.GetClass(field.Relation.TargetClass)
		if !ok {
			return op, fmt.Errorf("关系目标类不存在: %s", field.Relation.TargetClass)
		}
		op.create, err = my.rowsOfRaw(ctx, target, raw, field.Name+".create")
	}
	return op, err
}

// idWriters 主键列表转参数写入器：元素可为字面量或变量（经槽位参数化保持计划可缓存）；
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
		writers = append(writers, my.astWriter(child.Value))
	}
	return writers, nil
}

// astWriter 字面量/变量AST值的参数写入器
func (my *Dialect) astWriter(value *ast.Value) paramWriter {
	return func(c *compiler.Context) error { return my.buildParam(c, value) }
}

// valWriter 运行期值的参数写入器
func (my *Dialect) valWriter(value any) paramWriter {
	return func(c *compiler.Context) error {
		c.Write(my.Placeholder(c.AddParam(value)))
		return nil
	}
}

// rawWriters 运行期值列表转参数写入器
func rawWriters(my *Dialect, values []interface{}) []paramWriter {
	writers := make([]paramWriter, 0, len(values))
	for _, value := range values {
		writers = append(writers, my.valWriter(value))
	}
	return writers
}

// buildRelationOps 关系操作CTE：与主变更同语句原子完成
// connect: 一对多更新外键 / 多对多插中间表；disconnect: 置NULL / 删中间表；
// create: 内联创建子行（o2m带外键锚点，m2m先建目标行再建中间表关联）
func (my *Dialect) buildRelationOps(ctx *compiler.Context, class *protocol.Class, ops []relationOp) error {
	// 主CTE单行锚点：(SELECT 源列 FROM "表名CTE")
	anchor := func(rel *protocol.Relation) {
		sourceCol := scope{class: class}.column(rel.SourceField)
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
		if len(target.PrimaryKeys) != 1 {
			return fmt.Errorf("关系操作要求目标实体 %s 有单一主键", target.Name)
		}
		sc := scope{class: target}
		pk := sc.column(target.PrimaryKeys[0])

		if through := op.rel.Through; through != nil {
			// 多对多：中间表插入/删除/内联创建
			ctx.MarkTable(through.TableName)
			if len(op.connect) > 0 {
				ctx.Write(`, `).Quote(`__c_`, ctx.NextIndex()).Write(` AS (INSERT INTO `)
				tableRef(ctx, through.TableName)
				ctx.Write(` (`).Quote(through.SourceKey).Write(`, `).Quote(through.TargetKey).Write(`) `)
				if len(target.Scope) > 0 {
					// 目标有作用域：经SELECT校验目标行属当前作用域，防建立跨租户关联（中间表污染）
					ctx.Write(`SELECT `)
					anchor(op.rel)
					ctx.Write(`, `).Column(target.Table, pk).Write(` FROM `)
					tableRef(ctx, target.Table)
					ctx.Write(` WHERE `).Column(target.Table, pk).Write(` IN (`)
					if err := params(op.connect); err != nil {
						return err
					}
					ctx.Write(`)`)
					my.appendScope(ctx, target.Table, target.Scope)
					ctx.Write(`)`)
				} else {
					// 无作用域：INSERT VALUES（参数类型由目标列推断），目标存在性由外键约束保证
					ctx.Write(`VALUES `)
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
			}
			if len(op.disconnect) > 0 {
				ctx.Write(`, `).Quote(`__c_`, ctx.NextIndex()).Write(` AS (DELETE FROM `)
				tableRef(ctx, through.TableName)
				ctx.Write(` WHERE `).Quote(through.SourceKey).Write(` = `)
				anchor(op.rel)
				ctx.Write(` AND `).Quote(through.TargetKey).Write(` IN (`)
				if err := params(op.disconnect); err != nil {
					return err
				}
				ctx.Write(`))`)
			}
			if len(op.create) > 0 {
				// 先建目标行，再按RETURNING主键建中间表关联
				ctx.MarkTable(target.Table)
				made := ctx.NextIndex()
				ctx.Write(`, `).Quote(`__c_`, made).Write(` AS (`)
				my.applyScope(target, op.create) // 嵌套创建的子行也填作用域
				if _, err := my.writeInsertValues(ctx, target.Table, op.create); err != nil {
					return err
				}
				ctx.Write(` RETURNING `).Quote(pk).Write(`)`)
				ctx.Write(`, `).Quote(`__c_`, ctx.NextIndex()).Write(` AS (INSERT INTO `)
				tableRef(ctx, through.TableName)
				ctx.Write(` (`).Quote(through.SourceKey).Write(`, `).Quote(through.TargetKey).
					Write(`) SELECT `)
				anchor(op.rel)
				ctx.Write(`, `).Quote(pk).Write(` FROM `).Quote(`__c_`, made).Write(`)`)
			}
			continue
		}

		// 一对多：更新/解除/内联创建目标表行
		ctx.MarkTable(target.Table)
		fk := sc.column(op.rel.TargetField)
		if len(op.connect) > 0 {
			ctx.Write(`, `).Quote(`__c_`, ctx.NextIndex()).Write(` AS (UPDATE `)
			tableRef(ctx, target.Table)
			ctx.Write(` SET `).Quote(fk).Write(` = `)
			anchor(op.rel)
			ctx.Write(` WHERE `).Quote(pk).Write(` IN (`)
			if err := params(op.connect); err != nil {
				return err
			}
			ctx.Write(`)`)
			my.appendScope(ctx, target.Table, target.Scope) // 只能挂接当前作用域的目标行（防跨租户劫持）
			ctx.Write(`)`)
		}
		if len(op.disconnect) > 0 {
			ctx.Write(`, `).Quote(`__c_`, ctx.NextIndex()).Write(` AS (UPDATE `)
			tableRef(ctx, target.Table)
			ctx.Write(` SET `).Quote(fk).Write(` = NULL WHERE `).
				Quote(fk).Write(` = `)
			anchor(op.rel)
			ctx.Write(` AND `).Quote(pk).Write(` IN (`)
			if err := params(op.disconnect); err != nil {
				return err
			}
			ctx.Write(`)`)
			my.appendScope(ctx, target.Table, target.Scope)
			ctx.Write(`)`)
		}
		if len(op.create) > 0 {
			// 内联建子行：外键列取主CTE锚点（覆盖行内同名列）
			rel := op.rel
			for i := range op.create {
				row := &op.create[i]
				if _, exists := row.values[fk]; !exists {
					row.columns = append(row.columns, fk)
				}
				row.values[fk] = func(*compiler.Context) error {
					anchor(rel)
					return nil
				}
			}
			ctx.Write(`, `).Quote(`__c_`, ctx.NextIndex()).Write(` AS (`)
			my.applyScope(target, op.create) // 嵌套创建的子行也填作用域
			if _, err := my.writeInsertValues(ctx, target.Table, op.create); err != nil {
				return err
			}
			ctx.Write(`)`)
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
