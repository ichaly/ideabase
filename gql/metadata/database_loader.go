package metadata

import (
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/gql/internal"
	"gorm.io/gorm"
)

// DatabaseDialect 数据库方言接口，提供不同数据库的元数据加载实现
type DatabaseDialect interface {
	// GetMetadataQuery 获取元数据查询SQL
	GetMetadataQuery() (string, []interface{})
}

// tableInfo 表信息结构
type tableInfo struct {
	TableName        string `gorm:"column:table_name"`
	TableDescription string `gorm:"column:table_description"`
}

// columnInfo 列信息结构
type columnInfo struct {
	TableName         string `gorm:"column:table_name"`
	ColumnName        string `gorm:"column:column_name"`
	DataType          string `gorm:"column:data_type"`
	IsNullable        bool   `gorm:"column:is_nullable"`
	CharMaxLength     *int64 `gorm:"column:character_maximum_length"`
	NumericPrecision  *int64 `gorm:"column:numeric_precision"`
	NumericScale      *int64 `gorm:"column:numeric_scale"`
	ColumnDescription string `gorm:"column:column_description"`
}

// primaryKeyInfo 主键信息结构
type primaryKeyInfo struct {
	TableName  string `gorm:"column:table_name"`
	ColumnName string `gorm:"column:column_name"`
}

// foreignKeyInfo 外键信息结构
type foreignKeyInfo struct {
	SourceTable  string `gorm:"column:source_table"`
	SourceColumn string `gorm:"column:source_column"`
	TargetTable  string `gorm:"column:target_table"`
	TargetColumn string `gorm:"column:target_column"`
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

	// 元数据结果结构
	type metadataResult struct {
		Metadata struct {
			Tables      []tableInfo      `json:"tables"`
			Columns     []columnInfo     `json:"columns"`
			PrimaryKeys []primaryKeyInfo `json:"primaryKeys"`
			ForeignKeys []foreignKeyInfo `json:"foreignKeys"`
		} `gorm:"column:metadata"`
	}

	var result metadataResult
	if err := my.db.Raw(query, args...).Scan(&result).Error; err != nil {
		return nil, nil, nil, nil, fmt.Errorf("加载数据库元数据失败: %w", err)
	}

	// 检查返回数据有效性
	if len(result.Metadata.Tables) == 0 {
		return nil, nil, nil, nil, fmt.Errorf("未找到任何表信息，请检查schema配置: %s", my.schema)
	}

	return result.Metadata.Tables, result.Metadata.Columns,
		result.Metadata.PrimaryKeys, result.Metadata.ForeignKeys, nil
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

	// 初始化类结构
	for _, table := range tables {
		className := strings.ToLower(table.TableName)
		classes[className] = &internal.Class{
			Name:        className,
			Table:       table.TableName,
			Virtual:     false,
			Fields:      make(map[string]*internal.Field),
			PrimaryKeys: []string{},
			Description: table.TableDescription,
		}
	}

	// 初始化字段
	for _, column := range columns {
		tableName := strings.ToLower(column.TableName)
		class, ok := classes[tableName]
		if !ok {
			continue
		}

		fieldName := strings.ToLower(column.ColumnName)
		class.Fields[fieldName] = &internal.Field{
			Name:        fieldName,
			Column:      column.ColumnName,
			Type:        column.DataType,
			Virtual:     false,
			Nullable:    column.IsNullable,
			Description: column.ColumnDescription,
		}
	}

	// 设置主键
	for _, pk := range primaryKeys {
		tableName := strings.ToLower(pk.TableName)
		class, ok := classes[tableName]
		if !ok {
			continue
		}

		columnName := strings.ToLower(pk.ColumnName)
		field, ok := class.Fields[columnName]
		if !ok {
			continue
		}

		field.IsPrimary = true
		class.PrimaryKeys = append(class.PrimaryKeys, columnName)
	}

	// 设置外键关系
	for _, fk := range foreignKeys {
		sourceTable := strings.ToLower(fk.SourceTable)
		sourceColumn := strings.ToLower(fk.SourceColumn)
		targetTable := strings.ToLower(fk.TargetTable)
		targetColumn := strings.ToLower(fk.TargetColumn)

		// 获取源类和字段
		sourceClass, okSource := classes[sourceTable]
		if !okSource {
			continue
		}
		sourceField, okSourceField := sourceClass.Fields[sourceColumn]
		if !okSourceField {
			continue
		}

		// 创建外键信息
		foreignKey := &internal.ForeignKey{
			TableName:  targetTable,
			ColumnName: targetColumn,
			Kind:       internal.MANY_TO_ONE, // 默认为多对一
		}

		// 设置外键
		sourceField.ForeignKey = foreignKey

		// 添加到关系映射
		if _, ok := relationships[sourceTable]; !ok {
			relationships[sourceTable] = make(map[string]*internal.ForeignKey)
		}
		relationships[sourceTable][sourceColumn] = foreignKey
	}

	return classes, relationships, nil
}
