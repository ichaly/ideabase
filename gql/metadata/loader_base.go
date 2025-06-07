package metadata

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/protocol"
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
func (my *baseLoader) loadMeta(t protocol.Tree, query string, args []interface{}) error {
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

	// 组装Class结构，主索引为表名
	classMap := make(map[string]*internal.Class)
	for _, t := range tables {
		classMap[t.TableName] = &internal.Class{
			Name:        t.TableName,
			Table:       t.TableName,
			Fields:      make(map[string]*internal.Field),
			PrimaryKeys: []string{},
			Description: t.TableDescription,
		}
	}
	// 组装字段信息
	for _, c := range columns {
		if class, ok := classMap[c.TableName]; ok {
			class.Fields[c.ColumnName] = &internal.Field{
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
	// 处理外键关系，自动建立正反向引用
	// 遍历所有外键信息，为每个外键建立正向（多对一/递归）和反向（一对多/递归）关系
	// 通过relationKey/reverseKey避免重复处理同一对关系
	relations := make(map[string]bool)
	for _, fk := range foreignKeys {
		sourceTable := fk.SourceTable
		sourceColumn := fk.SourceColumn
		targetTable := fk.TargetTable
		targetColumn := fk.TargetColumn

		// 生成唯一关系标识符，避免正反向重复处理
		relationKey := fmt.Sprintf("%s.%s->%s.%s", sourceTable, sourceColumn, targetTable, targetColumn)
		reverseKey := fmt.Sprintf("%s.%s->%s.%s", targetTable, targetColumn, sourceTable, sourceColumn)

		// 如果已经处理过这个关系（正向或反向），跳过
		if relations[relationKey] || relations[reverseKey] {
			continue
		}

		// 标记为已处理
		relations[relationKey] = true
		relations[reverseKey] = true

		// 获取源类和字段
		sourceClass, ok := classMap[sourceTable]
		if !ok {
			continue
		}
		sourceField, ok := sourceClass.Fields[sourceColumn]
		if !ok {
			continue
		}

		// 获取目标类和字段
		targetClass, ok := classMap[targetTable]
		if !ok {
			continue
		}
		targetField, ok := targetClass.Fields[targetColumn]
		if !ok {
			continue
		}

		// 判断是否为自关联
		isRecursive := sourceTable == targetTable
		// 正向关系（多对一/递归）：如 comments.user_id -> users.id
		sourceField.Relation = &internal.Relation{
			SourceClass: sourceTable,
			SourceField: sourceColumn,
			TargetClass: targetTable,
			TargetField: targetColumn,
			Type:        lo.Ternary(isRecursive, internal.RECURSIVE, internal.MANY_TO_ONE),
		}
		// 反向关系（一对多/递归）：如 users.id <- comments.user_id
		targetField.Relation = &internal.Relation{
			SourceClass: targetTable,
			SourceField: targetColumn,
			TargetClass: sourceTable,
			TargetField: sourceColumn,
			Type:        lo.Ternary(isRecursive, internal.RECURSIVE, internal.ONE_TO_MANY),
		}
		// 建立双向引用，便于后续GraphQL编译和关系导航
		sourceField.Relation.Reverse = targetField.Relation
		targetField.Relation.Reverse = sourceField.Relation
	}
	// 处理多对多关系
	detectManyToManyRelations(classMap, foreignKeys, primaryKeys)
	// 注入Hoster，供后续GraphQL编译等使用
	for index, class := range classMap {
		if err := t.PutNode(index, class); err != nil {
			return fmt.Errorf("注入Hoster失败: %w", err)
		}
	}
	// 使用当前时间作为版本号
	t.SetVersion(time.Now().Format("20060102150405"))
	return nil
}

// detectManyToManyRelations 检测并处理多对多关系
// 1. 检查每个表的外键和主键，识别中间表
// 2. 为多对多关系自动建立Relation结构
func detectManyToManyRelations(classes map[string]*internal.Class, foreignKeys []foreignKeyInfo, primaryKeys []primaryKeyInfo) {
	tableToFKs := make(map[string][]foreignKeyInfo)
	for _, fk := range foreignKeys {
		tableToFKs[fk.SourceTable] = append(tableToFKs[fk.SourceTable], fk)
	}
	tableToPKs := make(map[string][]string)
	for _, pk := range primaryKeys {
		tableToPKs[pk.TableName] = append(tableToPKs[pk.TableName], pk.ColumnName)
	}
	for tableName, fks := range tableToFKs {
		// 仅包含两个外键的表才可能是中间表
		if len(fks) != 2 {
			continue
		}
		class := classes[tableName]
		if class == nil {
			continue
		}
		pks := tableToPKs[tableName]
		// 主键必须正好是这两个外键，或表名符合中间表命名规则
		if !containsSameElements(pks, []string{fks[0].SourceColumn, fks[1].SourceColumn}) {
			if !isThroughTableByName(tableName, fks[0].TargetTable, fks[1].TargetTable) {
				continue
			}
		}
		createManyToManyRelation(classes, tableName, fks[0], fks[1])
	}
}

// containsSameElements 检查两个字符串切片是否包含相同的元素(不考虑顺序)
func containsSameElements(a, b []string) bool {
	aCopy := append([]string(nil), a...)
	bCopy := append([]string(nil), b...)
	sort.Strings(aCopy)
	sort.Strings(bCopy)
	return reflect.DeepEqual(aCopy, bCopy)
}

// isThroughTableByName 判断表名是否为中间表
func isThroughTableByName(tableName, table1, table2 string) bool {
	expectedName1 := table1 + "_" + table2
	expectedName2 := table2 + "_" + table1
	return tableName == expectedName1 || tableName == expectedName2
}

// createManyToManyRelation 为多对多关系自动建立Relation结构
func createManyToManyRelation(classes map[string]*internal.Class, throughTable string, fk1, fk2 foreignKeyInfo) {
	class1, class2 := classes[fk1.TargetTable], classes[fk2.TargetTable]
	if class1 == nil || class2 == nil {
		return
	}
	if throughClass, exists := classes[throughTable]; exists {
		throughClass.IsThrough = true
	}
	createRelation := func(
		sourceTable, targetTable, sourceColumn, targetColumn, sourceKey, targetKey string, reverse *internal.Relation,
	) internal.Relation {
		return internal.Relation{
			SourceClass: sourceTable,
			SourceField: sourceColumn,
			TargetClass: targetTable,
			TargetField: targetColumn,
			Type:        internal.MANY_TO_MANY,
			Through: &internal.Through{
				Table:     throughTable,
				SourceKey: sourceKey,
				TargetKey: targetKey,
			},
			Reverse: reverse,
		}
	}
	r1, r2 := &internal.Relation{}, &internal.Relation{}
	*r1 = createRelation(fk1.TargetTable, fk2.TargetTable, fk1.TargetColumn, fk2.TargetColumn, fk1.SourceColumn, fk2.SourceColumn, r2)
	*r2 = createRelation(fk2.TargetTable, fk1.TargetTable, fk2.TargetColumn, fk1.TargetColumn, fk2.SourceColumn, fk1.SourceColumn, r1)
	if f1 := class1.Fields[fk1.TargetColumn]; f1 != nil && f1.IsPrimary {
		f1.Relation = r1
	}
	if f2 := class2.Fields[fk2.TargetColumn]; f2 != nil && f2.IsPrimary {
		f2.Relation = r2
	}
}
