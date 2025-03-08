package metadata

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/log"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

// NullableType 自定义类型，用于处理MySQL和PostgreSQL的可空字段
type NullableType bool

func (my NullableType) Bool() bool {
	return bool(my)
}

func (my *NullableType) UnmarshalJSON(data []byte) error {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	switch value := v.(type) {
	case bool:
		*my = NullableType(value)
	case float64:
		*my = value != 0
	case string:
		*my = value == "1" || value == "true"
	default:
		return fmt.Errorf("unexpected type for nullable: %T", v)
	}
	return nil
}

// DatabaseDialect 数据库方言接口，提供不同数据库的元数据加载实现
type DatabaseDialect interface {
	// GetMetadataQuery 获取元数据查询SQL
	GetMetadataQuery() (string, []interface{})
}

// tableInfo 表信息结构
type tableInfo struct {
	TableName        string `json:"table_name" gorm:"column:table_name"`
	TableDescription string `json:"table_description" gorm:"column:table_description"`
}

// columnInfo 列信息结构
type columnInfo struct {
	TableName         string       `json:"table_name" gorm:"column:table_name"`
	ColumnName        string       `json:"column_name" gorm:"column:column_name"`
	DataType          string       `json:"data_type" gorm:"column:data_type"`
	IsNullable        NullableType `json:"is_nullable" gorm:"column:is_nullable"`
	CharMaxLength     *int64       `json:"character_maximum_length" gorm:"column:character_maximum_length"`
	NumericPrecision  *int64       `json:"numeric_precision" gorm:"column:numeric_precision"`
	NumericScale      *int64       `json:"numeric_scale" gorm:"column:numeric_scale"`
	ColumnDescription string       `json:"column_description" gorm:"column:column_description"`
}

// primaryKeyInfo 主键信息结构
type primaryKeyInfo struct {
	TableName  string `json:"table_name" gorm:"column:table_name"`
	ColumnName string `json:"column_name" gorm:"column:column_name"`
}

// foreignKeyInfo 外键信息结构
type foreignKeyInfo struct {
	SourceTable  string `json:"source_table" gorm:"column:source_table"`
	SourceColumn string `json:"source_column" gorm:"column:source_column"`
	TargetTable  string `json:"target_table" gorm:"column:target_table"`
	TargetColumn string `json:"target_column" gorm:"column:target_column"`
}

// DatabaseLoader 数据库元数据加载器
type DatabaseLoader struct {
	db      *gorm.DB
	schema  string // 数据库schema名称
	dialect DatabaseDialect
}

// NewDatabaseLoader 创建新的数据库加载器
func NewDatabaseLoader(db *gorm.DB, schema string) (*DatabaseLoader, error) {
	if schema == "" {
		schema = "public" // 默认使用public schema
	}

	// 检测数据库类型并创建对应的方言实现
	dialectName := db.Dialector.Name()
	var dialect DatabaseDialect
	var err error

	if strings.ToLower(dialectName) == "mysql" {
		dialect, err = NewMySQLDialect(db, schema)
		if err != nil {
			return nil, fmt.Errorf("MySQL方言初始化失败: %w", err)
		}
	} else {
		// PostgreSQL或其他数据库使用PostgreSQL方言
		dialect, err = NewPostgresDialect(db, schema)
		if err != nil {
			return nil, fmt.Errorf("PostgreSQL方言初始化失败: %w", err)
		}
	}

	return &DatabaseLoader{
		db:      db,
		schema:  schema,
		dialect: dialect,
	}, nil
}

// loadMetadataFromDB 从数据库加载元数据
func (my *DatabaseLoader) loadMetadataFromDB() ([]tableInfo, []columnInfo, []primaryKeyInfo, []foreignKeyInfo, error) {
	// 获取方言特定的查询和参数
	query, args := my.dialect.GetMetadataQuery()

	// 执行查询
	var result struct {
		Tables      []tableInfo      `json:"tables"`
		Columns     []columnInfo     `json:"columns"`
		PrimaryKeys []primaryKeyInfo `json:"primaryKeys"`
		ForeignKeys []foreignKeyInfo `json:"foreignKeys"`
	}

	rows, err := my.db.Raw(query, args...).Rows()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("执行元数据查询失败: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, nil, nil, nil, fmt.Errorf("未获取到元数据结果")
	}

	var jsonData []byte
	if err := rows.Scan(&jsonData); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("扫描元数据结果失败: %w", err)
	}

	log.Debug().RawJSON("metadata", jsonData).Msg("从数据库加载的原始元数据")

	if err := json.Unmarshal(jsonData, &result); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("解析元数据JSON失败: %w", err)
	}

	// 检查返回数据有效性
	if len(result.Tables) == 0 {
		return nil, nil, nil, nil, fmt.Errorf("未找到任何表信息，请检查schema配置: %s", my.schema)
	}

	return result.Tables, result.Columns, result.PrimaryKeys, result.ForeignKeys, nil
}

// LoadMetadata 加载数据库元数据
func (my *DatabaseLoader) LoadMetadata() (map[string]*internal.Class, error) {
	// 创建结果容器
	classes := make(map[string]*internal.Class)

	// 从数据库加载元数据
	tables, columns, primaryKeys, foreignKeys, err := my.loadMetadataFromDB()
	if err != nil {
		return nil, err
	}

	// 初始化类结构
	for _, table := range tables {
		tableName := table.TableName
		classes[tableName] = &internal.Class{
			Name:        tableName,
			Table:       tableName,
			Virtual:     false,
			Fields:      make(map[string]*internal.Field),
			PrimaryKeys: []string{},
			Description: table.TableDescription,
		}
	}

	// 初始化字段
	for _, column := range columns {
		tableName := column.TableName
		columnName := column.ColumnName
		class, ok := classes[tableName]
		if !ok {
			continue
		}

		class.Fields[columnName] = &internal.Field{
			Name:        columnName,
			Column:      columnName,
			Type:        column.DataType,
			Virtual:     false,
			Nullable:    column.IsNullable.Bool(),
			Description: column.ColumnDescription,
		}
	}

	// 设置主键
	for _, pk := range primaryKeys {
		tableName := pk.TableName
		columnName := pk.ColumnName
		class, ok := classes[tableName]
		if !ok {
			continue
		}

		field, ok := class.Fields[columnName]
		if !ok {
			continue
		}

		field.IsPrimary = true
		class.PrimaryKeys = append(class.PrimaryKeys, columnName)
	}

	// 设置外键关系 - 优化处理逻辑，避免重复遍历
	relations := make(map[string]bool)
	for _, fk := range foreignKeys {
		sourceTable := fk.SourceTable
		sourceColumn := fk.SourceColumn
		targetTable := fk.TargetTable
		targetColumn := fk.TargetColumn

		// 生成唯一关系标识符
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
		sourceClass, ok := classes[sourceTable]
		if !ok {
			continue
		}

		sourceField, ok := sourceClass.Fields[sourceColumn]
		if !ok {
			continue
		}

		// 获取目标类和字段
		targetClass, ok := classes[targetTable]
		if !ok {
			continue
		}

		targetField, ok := targetClass.Fields[targetColumn]
		if !ok {
			continue
		}

		// 判断是否为自关联
		isRecursive := sourceTable == targetTable

		// 创建正向关系
		sourceField.Relation = &internal.Relation{
			SourceClass: sourceTable,
			SourceField: sourceColumn,
			TargetClass: targetTable,
			TargetField: targetColumn,
			Type:        lo.Ternary(isRecursive, internal.RECURSIVE, internal.MANY_TO_ONE),
		}

		// 创建反向关系
		targetField.Relation = &internal.Relation{
			SourceClass: targetTable,
			SourceField: targetColumn,
			TargetClass: sourceTable,
			TargetField: sourceColumn,
			Type:        lo.Ternary(isRecursive, internal.RECURSIVE, internal.ONE_TO_MANY),
		}

		// 设置双向引用
		sourceField.Relation.Reverse = targetField.Relation
		targetField.Relation.Reverse = sourceField.Relation
	}

	// 检测并处理多对多关系
	my.detectManyToManyRelations(classes, foreignKeys, primaryKeys)

	return classes, nil
}

// detectManyToManyRelations 检测并处理多对多关系
func (my *DatabaseLoader) detectManyToManyRelations(classes map[string]*internal.Class, foreignKeys []foreignKeyInfo, primaryKeys []primaryKeyInfo) {
	// 1. 收集每个表的外键信息
	tableToFKs := make(map[string][]foreignKeyInfo)
	for _, fk := range foreignKeys {
		tableToFKs[fk.SourceTable] = append(tableToFKs[fk.SourceTable], fk)
	}

	// 2. 收集每个表的主键信息
	tableToPKs := make(map[string][]string)
	for _, pk := range primaryKeys {
		tableToPKs[pk.TableName] = append(tableToPKs[pk.TableName], pk.ColumnName)
	}

	// 3. 检测可能的多对多关系表
	for tableName, fks := range tableToFKs {
		// 条件1: 表包含且仅包含两个外键
		if len(fks) != 2 {
			continue
		}

		class := classes[tableName]
		if class == nil {
			continue
		}

		// 条件2: 主键由这两个外键组成
		pks := tableToPKs[tableName]
		if !containsSameElements(pks, []string{fks[0].SourceColumn, fks[1].SourceColumn}) {
			// 不满足条件2，检查条件3
			// 条件3: 表名符合 table1_table2 格式
			if !isThroughTableByName(tableName, fks[0].TargetTable, fks[1].TargetTable) {
				continue
			}
		}

		// 识别为多对多关系表，创建多对多关系
		createManyToManyRelation(classes, tableName, fks[0], fks[1])
	}
}

// containsSameElements 检查两个字符串切片是否包含相同的元素(不考虑顺序)
func containsSameElements(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	// 创建map来跟踪元素出现次数
	counts := make(map[string]int)
	for _, item := range a {
		counts[item]++
	}

	// 检查b中的元素是否与a中的匹配
	for _, item := range b {
		counts[item]--
		if counts[item] < 0 {
			return false
		}
	}

	// 所有计数器应为0
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}

	return true
}

// isThroughTableByName 通过表名检查是否是中间表
func isThroughTableByName(tableName, table1, table2 string) bool {
	// 检查表名是否符合 table1_table2 或 table2_table1 格式
	expectedName1 := table1 + "_" + table2
	expectedName2 := table2 + "_" + table1

	return tableName == expectedName1 || tableName == expectedName2
}

// createManyToManyRelation 创建多对多关系
func createManyToManyRelation(classes map[string]*internal.Class, throughTable string, fk1, fk2 foreignKeyInfo) {
	// 获取相关类
	table1 := fk1.TargetTable
	table2 := fk2.TargetTable
	class1 := classes[table1]
	class2 := classes[table2]

	if class1 == nil || class2 == nil {
		return
	}

	// 创建中间表配置
	through1 := &internal.Through{
		Table:     throughTable,
		SourceKey: fk1.SourceColumn,
		TargetKey: fk2.SourceColumn,
	}

	through2 := &internal.Through{
		Table:     throughTable,
		SourceKey: fk2.SourceColumn,
		TargetKey: fk1.SourceColumn,
	}

	// 创建从表1到表2的多对多关系的虚拟字段
	field1Name := getVirtualFieldName(table2)
	field1 := &internal.Field{
		Name:    field1Name,
		Virtual: true,
		Relation: &internal.Relation{
			SourceClass: table1,
			SourceField: fk1.TargetColumn,
			TargetClass: table2,
			TargetField: fk2.TargetColumn,
			Type:        internal.MANY_TO_MANY,
			Through:     through1,
		},
	}

	// 创建从表2到表1的多对多关系的虚拟字段
	field2Name := getVirtualFieldName(table1)
	field2 := &internal.Field{
		Name:    field2Name,
		Virtual: true,
		Relation: &internal.Relation{
			SourceClass: table2,
			SourceField: fk2.TargetColumn,
			TargetClass: table1,
			TargetField: fk1.TargetColumn,
			Type:        internal.MANY_TO_MANY,
			Through:     through2,
		},
	}

	// 设置双向引用
	field1.Relation.Reverse = field2.Relation
	field2.Relation.Reverse = field1.Relation

	// 添加虚拟字段到各自的类
	class1.Fields[field1Name] = field1
	class2.Fields[field2Name] = field2
}

// getVirtualFieldName 获取多对多关系的虚拟字段名
func getVirtualFieldName(tableName string) string {
	// 默认使用目标表名作为关系字段名
	// 可以进行复数化或其他转换
	return tableName + "List"
}
