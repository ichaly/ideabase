package metadata

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/gql/internal"
	"gorm.io/gorm"
)

// NullableType 自定义类型，用于处理MySQL和PostgreSQL的可空字段
type NullableType bool

func (n NullableType) Bool() bool {
	return bool(n)
}

func (n *NullableType) UnmarshalJSON(data []byte) error {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	switch value := v.(type) {
	case bool:
		*n = NullableType(value)
	case float64:
		*n = value != 0
	case string:
		*n = value == "1" || value == "true"
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

	fmt.Printf("原始JSON数据: %s\n", string(jsonData))

	if err := json.Unmarshal(jsonData, &result); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("解析元数据JSON失败: %w", err)
	}

	// 将所有表名和字段名转换为小写
	for i := range result.Tables {
		result.Tables[i].TableName = strings.ToLower(result.Tables[i].TableName)
	}
	for i := range result.Columns {
		result.Columns[i].TableName = strings.ToLower(result.Columns[i].TableName)
		result.Columns[i].ColumnName = strings.ToLower(result.Columns[i].ColumnName)
	}
	for i := range result.PrimaryKeys {
		result.PrimaryKeys[i].TableName = strings.ToLower(result.PrimaryKeys[i].TableName)
		result.PrimaryKeys[i].ColumnName = strings.ToLower(result.PrimaryKeys[i].ColumnName)
	}
	for i := range result.ForeignKeys {
		result.ForeignKeys[i].SourceTable = strings.ToLower(result.ForeignKeys[i].SourceTable)
		result.ForeignKeys[i].SourceColumn = strings.ToLower(result.ForeignKeys[i].SourceColumn)
		result.ForeignKeys[i].TargetTable = strings.ToLower(result.ForeignKeys[i].TargetTable)
		result.ForeignKeys[i].TargetColumn = strings.ToLower(result.ForeignKeys[i].TargetColumn)
	}

	// 检查返回数据有效性
	if len(result.Tables) == 0 {
		return nil, nil, nil, nil, fmt.Errorf("未找到任何表信息，请检查schema配置: %s", my.schema)
	}

	return result.Tables, result.Columns, result.PrimaryKeys, result.ForeignKeys, nil
}

// LoadMetadata 加载数据库元数据
func (my *DatabaseLoader) LoadMetadata() (map[string]*internal.Class, map[string]map[string]*internal.ForeignKey, error) {
	// 创建结果容器
	classes := make(map[string]*internal.Class)
	relationships := make(map[string]map[string]*internal.ForeignKey)

	// 从数据库加载元数据
	tables, columns, primaryKeys, foreignKeys, err := my.loadMetadataFromDB()
	if err != nil {
		return nil, nil, err
	}

	fmt.Printf("加载到的表信息: %+v\n", tables)
	fmt.Printf("加载到的列信息: %+v\n", columns)
	fmt.Printf("加载到的主键信息: %+v\n", primaryKeys)
	fmt.Printf("加载到的外键信息: %+v\n", foreignKeys)

	// 初始化类结构
	for _, table := range tables {
		tableName := strings.ToLower(table.TableName)
		classes[tableName] = &internal.Class{
			Name:        tableName,
			Table:       tableName,
			Virtual:     false,
			Fields:      make(map[string]*internal.Field),
			PrimaryKeys: []string{},
			Description: table.TableDescription,
		}
	}

	fmt.Printf("初始化的类结构: %+v\n", classes)

	// 初始化字段
	for _, column := range columns {
		tableName := strings.ToLower(column.TableName)
		columnName := strings.ToLower(column.ColumnName)
		class, ok := classes[tableName]
		if !ok {
			fmt.Printf("未找到表 %s 的类定义\n", tableName)
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

	fmt.Printf("添加字段后的类结构: %+v\n", classes)

	// 设置主键
	for _, pk := range primaryKeys {
		tableName := strings.ToLower(pk.TableName)
		columnName := strings.ToLower(pk.ColumnName)
		class, ok := classes[tableName]
		if !ok {
			fmt.Printf("未找到表 %s 的类定义（主键）\n", tableName)
			continue
		}

		field, ok := class.Fields[columnName]
		if !ok {
			fmt.Printf("未找到表 %s 的字段 %s（主键）\n", tableName, columnName)
			continue
		}

		field.IsPrimary = true
		class.PrimaryKeys = append(class.PrimaryKeys, columnName)
	}

	fmt.Printf("添加主键后的类结构: %+v\n", classes)

	// 设置外键关系
	for _, fk := range foreignKeys {
		sourceTable := strings.ToLower(fk.SourceTable)
		sourceColumn := strings.ToLower(fk.SourceColumn)
		targetTable := strings.ToLower(fk.TargetTable)
		targetColumn := strings.ToLower(fk.TargetColumn)

		fmt.Printf("处理外键关系: %s.%s -> %s.%s\n", sourceTable, sourceColumn, targetTable, targetColumn)

		// 获取源类和字段
		sourceClass, okSource := classes[sourceTable]
		if !okSource {
			fmt.Printf("未找到表 %s 的类定义（外键）\n", sourceTable)
			continue
		}
		sourceField, okSourceField := sourceClass.Fields[sourceColumn]
		if !okSourceField {
			fmt.Printf("未找到表 %s 的字段 %s（外键）\n", sourceTable, sourceColumn)
			continue
		}

		// 初始化关系映射
		if _, ok := relationships[sourceTable]; !ok {
			relationships[sourceTable] = make(map[string]*internal.ForeignKey)
		}

		// 创建外键信息
		foreignKey := &internal.ForeignKey{
			TableName:  targetTable,
			ColumnName: targetColumn,
			Kind:       internal.MANY_TO_ONE, // 默认为多对一
		}

		// 设置外键
		relationships[sourceTable][sourceColumn] = foreignKey
		sourceField.ForeignKey = foreignKey
	}

	fmt.Printf("最终的类结构: %+v\n", classes)
	fmt.Printf("最终的关系结构: %+v\n", relationships)

	return classes, relationships, nil
}
