package metadata

import (
	"encoding/json"
	"fmt"
	"github.com/ichaly/ideabase/gql/internal"
	"sort"
	"time"

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

	var (
		tables      []tableInfo
		columns     []columnInfo
		primaryKeys []primaryKeyInfo
		foreignKeys []foreignKeyInfo
	)
	// 假设rows只返回一行JSON，解析为结构体
	if rows.Next() {
		var jsonData []byte
		if err := rows.Scan(&jsonData); err != nil {
			return fmt.Errorf("扫描元数据结果失败: %w", err)
		}
		// 解析JSON为结构体
		if err := json.Unmarshal(jsonData, &struct {
			Tables      *[]tableInfo      `json:"tables"`
			Columns     *[]columnInfo     `json:"columns"`
			PrimaryKeys *[]primaryKeyInfo `json:"primaryKeys"`
			ForeignKeys *[]foreignKeyInfo `json:"foreignKeys"`
		}{
			&tables, &columns, &primaryKeys, &foreignKeys,
		}); err != nil {
			return fmt.Errorf("解析元数据JSON失败: %w", err)
		}
	}

	// 外键按(表,约束,列序)排序：复合外键列序不可乱（与目标列按位对齐），
	// 中间表fks[0/1]判定与关系生成顺序跨方言跨启动确定
	// （MySQL的JSON_ARRAYAGG不支持聚合内ORDER BY，无法在SQL层保证）
	fkKey := func(fk foreignKeyInfo) string {
		return fmt.Sprintf("%s\x00%s\x00%03d\x00%s", fk.SourceTable, fk.ConstraintName, fk.Position, fk.SourceColumn)
	}
	sort.Slice(foreignKeys, func(i, j int) bool { return fkKey(foreignKeys[i]) < fkKey(foreignKeys[j]) })

	// 组装Class结构，主索引为表名
	classMap := make(map[string]*protocol.Class)
	for _, t := range tables {
		classMap[t.TableName] = &protocol.Class{
			Name:        t.TableName,
			Table:       t.TableName,
			Fields:      make(map[string]*protocol.Field),
			PrimaryKeys: []string{},
			Description: t.TableDescription,
		}
	}
	// 组装字段信息
	for _, c := range columns {
		if class, ok := classMap[c.TableName]; ok {
			class.Fields[c.ColumnName] = &protocol.Field{
				Name:        c.ColumnName,
				Column:      c.ColumnName,
				Type:        c.DataType,
				Nullable:    c.IsNullable.Bool(),
				Description: c.ColumnDescription,
			}
		}
	}
	// 处理主键信息
	for _, pk := range primaryKeys {
		if class, ok := classMap[pk.TableName]; ok {
			if field, ok := class.Fields[pk.ColumnName]; ok {
				field.IsPrimary = true
				class.PrimaryKeys = append(class.PrimaryKeys, pk.ColumnName)
			}
		}
	}
	// 处理外键：按约束聚合（复合外键多行同名收敛为一条关系），关系为一等对象
	// 进类级Relations集合——同表多外键、复合键、自引用互不覆盖；
	// 单列关系同时在外键列挂兼容指针（finalize的ID定型与渲染消费）
	groups := groupByConstraint(foreignKeys)
	for _, g := range groups {
		sourceClass, ok := classMap[g[0].SourceTable]
		if !ok {
			continue
		}
		targetClass, ok := classMap[g[0].TargetTable]
		if !ok {
			continue
		}
		if !columnsExist(sourceClass, targetClass, g) {
			continue
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
		reverse := &protocol.Relation{
			Name:        g[0].ConstraintName,
			SourceClass: g[0].TargetTable,
			SourceField: targetCols[0],
			TargetClass: g[0].SourceTable,
			TargetField: sourceCols[0],
			Type:        protocol.ONE_TO_MANY,
		}
		if composite {
			reverse.SourceFields, reverse.TargetFields = targetCols, sourceCols
		}
		targetClass.AddRelation(reverse)
	}
	// 处理多对多关系
	detectManyToManyRelations(classMap, groups, primaryKeys)
	// 注入Hoster，供后续GraphQL编译等使用
	for index, class := range classMap {
		if err := h.PutNode(index, class); err != nil {
			return fmt.Errorf("注入Hoster失败: %w", err)
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
