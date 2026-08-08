package metadata

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/log"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

// baseLoader 只在包内复用，封装数据库元数据加载的主流程
// 不对外暴露
type baseLoader struct {
	db  *gorm.DB
	cfg *internal.Config
}

type catalogData struct {
	Tables      []tableInfo      `json:"tables"`
	Columns     []columnInfo     `json:"columns"`
	PrimaryKeys []primaryKeyInfo `json:"primaryKeys"`
	ForeignKeys []foreignKeyInfo `json:"foreignKeys"`
	Constraints []constraintInfo `json:"constraints"`
	Indexes     []indexInfo      `json:"indexes"`
	Routines    []routineInfo    `json:"routines"`
}

// loadMeta 通用数据库元数据加载主流程
// 1. 执行SQL获取元数据JSON
// 2. 解析为tableInfo/columnInfo等结构体
// 3. 组装为Class结构，处理主键、外键、多对多关系
// 4. 注入Hoster
func (my *baseLoader) loadMeta(h protocol.Hoster, query string, args []interface{}) error {
	rows, err := my.db.Raw(query, args...).Rows()
	if err != nil {
		return fmt.Errorf("执行元数据SQL失败: %w", err)
	}
	defer rows.Close()

	var catalog catalogData
	// 假设rows只返回一行JSON，解析为结构体
	if rows.Next() {
		var jsonData []byte
		if err := rows.Scan(&jsonData); err != nil {
			return fmt.Errorf("扫描元数据结果失败: %w", err)
		}
		// 解析JSON为结构体
		if err := json.Unmarshal(jsonData, &catalog); err != nil {
			return fmt.Errorf("解析元数据JSON失败: %w", err)
		}
	}
	return my.loadCatalog(h, catalog)
}

func (my *baseLoader) loadCatalog(h protocol.Hoster, catalog catalogData) error {
	tables, columns := catalog.Tables, catalog.Columns
	primaryKeys, foreignKeys := catalog.PrimaryKeys, catalog.ForeignKeys
	constraints, indexes, routines := catalog.Constraints, catalog.Indexes, catalog.Routines

	// 组装Class结构，主索引为表名
	classMap := make(map[string]*protocol.Class)
	for _, t := range tables {
		classMap[t.TableName] = &protocol.Class{
			Name:        t.TableName,
			Table:       t.TableName,
			Fields:      make(map[string]*protocol.Field),
			PrimaryKeys: []string{},
			Description: t.TableDescription,
			Schema:      t.Schema,
			TableKind:   protocol.TableKind(t.TableKind),
			ReadOnly:    protocol.TableKind(t.TableKind).ReadOnly(),
			Hidden:      t.TableName == "gql_audit_outbox",
		}
	}
	// 组装字段信息
	for _, c := range columns {
		if class, ok := classMap[c.TableName]; ok {
			field := &protocol.Field{
				Name:        c.ColumnName,
				Column:      c.ColumnName,
				Type:        c.DataType,
				Nullable:    c.IsNullable.Bool(),
				Description: c.ColumnDescription,
			}
			if c.TypeKind != "" {
				field.Catalog = &protocol.Column{Ordinal: c.Ordinal, Type: protocol.TypeRef{
					Schema: c.TypeSchema, Name: c.TypeName, Kind: protocol.TypeKind(c.TypeKind),
					Base: c.BaseType, EnumLabels: c.EnumLabels,
				}}
			}
			if c.ElementName != "" && field.Catalog != nil {
				field.Catalog.Type.Element = &protocol.TypeRef{
					Schema: c.ElementSchema, Name: c.ElementName, Kind: protocol.TypeKind(c.ElementKind), Base: c.ElementBase,
					EnumLabels: c.EnumLabels,
				}
			}
			if c.DefaultExpression != "" {
				if field.Catalog == nil {
					field.Catalog = &protocol.Column{Ordinal: c.Ordinal}
				}
				field.Catalog.Default = &protocol.ColumnDefault{Expression: c.DefaultExpression}
			}
			if c.GenerationKind != "" {
				if field.Catalog == nil {
					field.Catalog = &protocol.Column{Ordinal: c.Ordinal}
				}
				field.Catalog.Generation = &protocol.ColumnGeneration{
					Kind: protocol.GenerationKind(c.GenerationKind), Expression: c.GenerationExpression,
				}
				field.ReadOnly = true
			}
			class.Fields[c.ColumnName] = field
		} else {
			return fmt.Errorf("catalog column references missing table %q", c.TableName)
		}
	}
	for _, class := range classMap {
		if class.Fields["deleted_at"] != nil {
			class.SoftDelete = &protocol.SoftDelete{Column: "deleted_at"}
			class.Where = append(class.Where, protocol.Predicate{Column: "deleted_at", Operator: "is-null"})
		}
	}
	for _, ci := range constraints {
		class, ok := classMap[ci.TableName]
		if !ok {
			return fmt.Errorf("catalog constraint %q references missing table %q", ci.Name, ci.TableName)
		}
		if ci.ReferencedTable != "" {
			if _, ok := classMap[ci.ReferencedTable]; !ok {
				return fmt.Errorf("catalog constraint %q references missing table %q", ci.Name, ci.ReferencedTable)
			}
			for _, col := range ci.ReferencedColumns {
				if classMap[ci.ReferencedTable].Fields[col] == nil {
					return fmt.Errorf("catalog constraint %q references missing column %q", ci.Name, col)
				}
			}
		}
		for _, col := range ci.Columns {
			if class.Fields[col] == nil {
				return fmt.Errorf("catalog constraint %q references missing column %q", ci.Name, col)
			}
		}
		constraint := &protocol.Constraint{
			Name: ci.Name, Kind: protocol.ConstraintKind(ci.Kind), Columns: ci.Columns,
			ReferencedTable: ci.ReferencedTable, ReferencedColumns: ci.ReferencedColumns,
			Expression: ci.Expression, Deferrable: ci.Deferrable, Validated: ci.Validated,
			OnUpdate: ci.OnUpdate, OnDelete: ci.OnDelete, Match: ci.Match,
		}
		class.Constraints = append(class.Constraints, constraint)
		switch constraint.Kind {
		case protocol.ConstraintPrimary:
			for _, column := range constraint.Columns {
				class.Fields[column].IsPrimary = true
				class.PrimaryKeys = append(class.PrimaryKeys, column)
				primaryKeys = append(primaryKeys, primaryKeyInfo{TableName: ci.TableName, ColumnName: column})
			}
		case protocol.ConstraintUnique:
			if len(constraint.Columns) == 1 {
				class.Fields[constraint.Columns[0]].IsUnique = true
			}
		case protocol.ConstraintForeign:
			if len(constraint.Columns) == 0 || len(constraint.Columns) != len(constraint.ReferencedColumns) {
				return fmt.Errorf("catalog foreign key %q has mismatched columns", constraint.Name)
			}
			for position, column := range constraint.Columns {
				foreignKeys = append(foreignKeys, foreignKeyInfo{
					SourceTable: ci.TableName, SourceColumn: column,
					TargetTable: ci.ReferencedTable, TargetColumn: ci.ReferencedColumns[position],
					ConstraintName: ci.Name, Position: position + 1,
				})
			}
		}
	}
	for _, ii := range indexes {
		class, ok := classMap[ii.TableName]
		if !ok {
			return fmt.Errorf("catalog index %q references missing table %q", ii.Name, ii.TableName)
		}
		keys := make([]protocol.IndexKey, len(ii.Keys))
		for i, key := range ii.Keys {
			if key.Column != "" && class.Fields[key.Column] == nil {
				return fmt.Errorf("catalog index %q references missing column %q", ii.Name, key.Column)
			}
			if key.Column == "" && key.Expression == "" {
				return fmt.Errorf("catalog index %q contains an empty key", ii.Name)
			}
			keys[i] = protocol.IndexKey{Column: key.Column, Expression: key.Expression}
		}
		for _, column := range ii.Include {
			if class.Fields[column] == nil {
				return fmt.Errorf("catalog index %q includes missing column %q", ii.Name, column)
			}
		}
		index := &protocol.Index{Name: ii.Name, Keys: keys, Unique: ii.Unique, Primary: ii.Primary, Invalid: ii.Invalid, Deferred: ii.Deferred, Predicate: ii.Predicate, Method: ii.Method, Include: ii.Include}
		class.Indexes = append(class.Indexes, index)
		if index.IsUsableConflictTarget() && len(index.Keys) == 1 && !index.Primary {
			class.Fields[index.Keys[0].Column].IsUnique = true
		}
	}
	// 处理主键信息
	for _, pk := range primaryKeys {
		if class, ok := classMap[pk.TableName]; ok {
			field, exists := class.Fields[pk.ColumnName]
			if !exists {
				return fmt.Errorf("catalog primary key references missing column %q", pk.ColumnName)
			}
			field.IsPrimary = true
			if !lo.Contains(class.PrimaryKeys, pk.ColumnName) {
				class.PrimaryKeys = append(class.PrimaryKeys, pk.ColumnName)
			}
		} else {
			return fmt.Errorf("catalog primary key references missing table %q", pk.TableName)
		}
	}
	// 外键包含方言兼容行和上面从一等约束派生的行，统一按位置排序。
	fkKey := func(fk foreignKeyInfo) string {
		return fmt.Sprintf("%s\x00%s\x00%06d\x00%s", fk.SourceTable, fk.ConstraintName, fk.Position, fk.SourceColumn)
	}
	sort.Slice(foreignKeys, func(i, j int) bool { return fkKey(foreignKeys[i]) < fkKey(foreignKeys[j]) })
	// 处理外键：按约束聚合（复合外键多行同名收敛为一条关系），关系为一等对象
	// 进类级Relations集合——同表多外键、复合键、自引用互不覆盖；
	// 单列关系同时在外键列挂兼容指针（finalize的ID定型与渲染消费）
	groups := groupByConstraint(foreignKeys)
	for _, g := range groups {
		sourceClass, ok := classMap[g[0].SourceTable]
		if !ok {
			return fmt.Errorf("catalog foreign key %q references missing source table %q", g[0].ConstraintName, g[0].SourceTable)
		}
		targetClass, ok := classMap[g[0].TargetTable]
		if !ok {
			return fmt.Errorf("catalog foreign key %q references missing target table %q", g[0].ConstraintName, g[0].TargetTable)
		}
		if !columnsExist(sourceClass, targetClass, g) {
			return fmt.Errorf("catalog foreign key %q references missing column", g[0].ConstraintName)
		}

		sourceCols := lo.Map(g, func(fk foreignKeyInfo, _ int) string { return fk.SourceColumn })
		targetCols := lo.Map(g, func(fk foreignKeyInfo, _ int) string { return fk.TargetColumn })
		isRecursive := g[0].SourceTable == g[0].TargetTable
		composite := len(g) > 1
		if isRecursive && composite {
			log.Warn().Str("table", g[0].SourceTable).Str("constraint", g[0].ConstraintName).
				Msg("复合自引用外键暂不支持递归关系，已跳过")
			continue
		}

		// 正向关系（多对一/递归）：如 comments.user_id -> users.id
		forward := &protocol.Relation{
			Name:        g[0].ConstraintName,
			SourceClass: g[0].SourceTable,
			SourceField: sourceCols[0],
			TargetClass: g[0].TargetTable,
			TargetField: targetCols[0],
			Type:        lo.Ternary(isRecursive, protocol.RECURSIVE, protocol.MANY_TO_ONE),
		}
		if composite {
			forward.SourceFields, forward.TargetFields = sourceCols, targetCols
		}
		sourceClass.AddRelation(forward)
		if field := sourceClass.Fields[sourceCols[0]]; !composite && field.Relation == nil {
			field.Relation = forward
		}
		if isRecursive {
			continue // 递归关系单条承载（外键→主键方向），parent/children/全树字段由此派生
		}

		// 反向关系（一对多）：如 users.id <- comments.user_id
		targetClass.AddRelation(forward.Clone(protocol.ONE_TO_MANY, true))
	}
	// 处理多对多关系
	detectManyToManyRelations(classMap, groups, primaryKeys)
	// 注入Hoster，供后续GraphQL编译等使用
	for index, class := range classMap {
		if err := h.PutNode(index, class); err != nil {
			return fmt.Errorf("注入Hoster失败: %w", err)
		}
	}
	if rh, ok := h.(protocol.RoutineHoster); ok {
		for _, info := range routines {
			arguments := make([]protocol.RoutineArgument, len(info.Arguments))
			for i, argument := range info.Arguments {
				arguments[i] = protocol.RoutineArgument{Name: argument.Name, Mode: argument.Mode, Type: argument.Type, Default: argument.Default}
			}
			routine := &protocol.Routine{Schema: info.Schema, Name: info.Name, Kind: protocol.RoutineKind(info.Kind), Identity: info.IdentityArguments, Arguments: arguments, ReturnType: info.ReturnType, ReturnsSet: info.ReturnsSet, Volatility: protocol.RoutineVolatility(info.Volatility), SecurityDefiner: info.SecurityDefiner}
			key := routine.Schema + "." + routine.Name + "(" + routine.Identity + ")"
			if err := rh.PutRoutine(key, routine); err != nil {
				return fmt.Errorf("注入Routine失败: %w", err)
			}
		}
	}
	// 使用当前时间作为版本号
	h.SetVersion(time.Now().Format("20060102150405"))
	return nil
}

// groupByConstraint 外键行按(表,约束)聚合，复合外键多行收敛为一组（列序已排定）；
// 约束名缺失时每行独立成组（单列语义，兼容无名来源）
func groupByConstraint(foreignKeys []foreignKeyInfo) [][]foreignKeyInfo {
	var groups [][]foreignKeyInfo
	byKey := make(map[string]int)
	for _, fk := range foreignKeys {
		if fk.ConstraintName == "" {
			groups = append(groups, []foreignKeyInfo{fk})
			continue
		}
		key := fk.SourceTable + "\x00" + fk.ConstraintName
		if idx, ok := byKey[key]; ok {
			groups[idx] = append(groups[idx], fk)
			continue
		}
		byKey[key] = len(groups)
		groups = append(groups, []foreignKeyInfo{fk})
	}
	return groups
}

// columnsExist 校验约束组的源/目标列在类定义中都存在
func columnsExist(source, target *protocol.Class, group []foreignKeyInfo) bool {
	for _, fk := range group {
		if source.Fields[fk.SourceColumn] == nil || target.Fields[fk.TargetColumn] == nil {
			return false
		}
	}
	return true
}

// detectManyToManyRelations 检测并处理多对多关系
// 1. 按外键约束数识别中间表（恰好两条单列外键约束）
// 2. 为多对多关系自动建立Relation结构（自引用时两方向各一条，互不覆盖）
func detectManyToManyRelations(classes map[string]*protocol.Class, groups [][]foreignKeyInfo, primaryKeys []primaryKeyInfo) {
	tableToFKs := make(map[string][][]foreignKeyInfo)
	for _, g := range groups {
		tableToFKs[g[0].SourceTable] = append(tableToFKs[g[0].SourceTable], g)
	}
	tableToPKs := make(map[string][]string)
	for _, pk := range primaryKeys {
		tableToPKs[pk.TableName] = append(tableToPKs[pk.TableName], pk.ColumnName)
	}
	for tableName, fks := range tableToFKs {
		// 仅包含两条单列外键约束的表才可能是中间表（复合外键的中间表暂不支持）
		if len(fks) != 2 || len(fks[0]) != 1 || len(fks[1]) != 1 {
			continue
		}
		class := classes[tableName]
		if class == nil {
			continue
		}
		pks := tableToPKs[tableName]
		// 主键必须正好是这两个外键，或表名符合中间表命名规则
		if !(lo.Every(pks, []string{fks[0][0].SourceColumn, fks[1][0].SourceColumn}) && len(pks) == 2) {
			if !isThroughTableByName(tableName, fks[0][0].TargetTable, fks[1][0].TargetTable) {
				continue
			}
		}
		createManyToManyRelation(classes, tableName, fks[0][0], fks[1][0])
	}
}

// isThroughTableByName 判断表名是否为中间表
func isThroughTableByName(tableName, table1, table2 string) bool {
	expectedName1 := table1 + "_" + table2
	expectedName2 := table2 + "_" + table1
	return tableName == expectedName1 || tableName == expectedName2
}

// createManyToManyRelation 为多对多关系自动建立Relation结构
func createManyToManyRelation(classes map[string]*protocol.Class, tableName string, fk1, fk2 foreignKeyInfo) {
	class1, class2 := classes[fk1.TargetTable], classes[fk2.TargetTable]
	if class1 == nil || class2 == nil {
		return
	}
	if throughClass, exists := classes[tableName]; exists {
		throughClass.IsThrough = true
	}
	createRelation := func(
		sourceTable, targetTable, sourceColumn, targetColumn, sourceKey, targetKey string,
	) protocol.Relation {
		return protocol.Relation{
			SourceClass: sourceTable,
			SourceField: sourceColumn,
			TargetClass: targetTable,
			TargetField: targetColumn,
			Type:        protocol.MANY_TO_MANY,
			Through: &protocol.Through{
				TableName: tableName,
				SourceKey: sourceKey,
				TargetKey: targetKey,
			},
		}
	}
	r1 := createRelation(fk1.TargetTable, fk2.TargetTable, fk1.TargetColumn, fk2.TargetColumn, fk1.SourceColumn, fk2.SourceColumn)
	r2 := createRelation(fk2.TargetTable, fk1.TargetTable, fk2.TargetColumn, fk1.TargetColumn, fk2.SourceColumn, fk1.SourceColumn)
	r1.Name, r2.Name = tableName+":"+fk1.SourceColumn, tableName+":"+fk2.SourceColumn
	// 类级集合两方向各一条：自引用时class1==class2，AddRelation按身份键并存不覆盖
	class1.AddRelation(&r1)
	class2.AddRelation(&r2)
}
