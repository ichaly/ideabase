package metadata

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// mysqlVersionQuery 获取MySQL版本的查询
const mysqlVersionQuery = "SELECT VERSION()"

// isMySQLVersionSupported 检查MySQL版本是否为8.0及以上
func isMySQLVersionSupported(db *gorm.DB) (bool, error) {
	var version string
	if err := db.Raw(mysqlVersionQuery).Scan(&version).Error; err != nil {
		return false, fmt.Errorf("获取MySQL版本失败: %w", err)
	}

	// 提取主版本号
	parts := strings.Split(version, ".")
	if len(parts) == 0 {
		return false, fmt.Errorf("无效的MySQL版本格式: %s", version)
	}

	mainVersionStr := parts[0]
	var mainVersion int
	if _, err := fmt.Sscanf(mainVersionStr, "%d", &mainVersion); err != nil {
		return false, fmt.Errorf("解析MySQL版本失败: %w", err)
	}

	return mainVersion >= 8, nil
}

// MySQLDialect MySQL 8.0+方言实现
type MySQLDialect struct {
	schema string
}

// NewMySQLDialect 创建MySQL方言实例
func NewMySQLDialect(db *gorm.DB, schema string) (DatabaseDialect, error) {
	isModern, err := isMySQLVersionSupported(db)
	if err != nil {
		return nil, fmt.Errorf("MySQL版本检测失败: %w", err)
	}

	if !isModern {
		return nil, fmt.Errorf("不支持的MySQL版本，需要MySQL 8.0或以上版本")
	}

	fmt.Println("检测到MySQL 8.0+版本，使用现代方言")
	return &MySQLDialect{
		schema: schema,
	}, nil
}

// MySQL的单条元数据查询（需要MySQL 8.0+）
const mysqlMetadataQuery = `
WITH 
tables AS (
    SELECT 
        table_name,
        table_comment as table_description
    FROM 
        information_schema.tables
    WHERE 
        table_schema = ?
        AND table_type = 'BASE TABLE'
),
columns AS (
    SELECT 
        table_name,
        column_name,
        data_type,
        is_nullable = 'YES' as is_nullable,
        character_maximum_length,
        numeric_precision,
        numeric_scale,
        column_comment as column_description
    FROM 
        information_schema.columns
    WHERE 
        table_schema = ?
),
primary_keys AS (
    SELECT 
        k.table_name,
        k.column_name
    FROM 
        information_schema.table_constraints t
    JOIN 
        information_schema.key_column_usage k ON t.constraint_name = k.constraint_name
    WHERE 
        t.constraint_type = 'PRIMARY KEY'
        AND t.table_schema = ?
),
foreign_keys AS (
    SELECT 
        k.table_name as source_table,
        k.column_name as source_column,
        r.referenced_table_name as target_table,
        r.referenced_column_name as target_column
    FROM 
        information_schema.key_column_usage k
    JOIN 
        information_schema.referential_constraints r ON k.constraint_name = r.constraint_name
    WHERE 
        k.constraint_schema = ?
        AND r.constraint_schema = ?
        AND r.referenced_table_name IS NOT NULL
)
SELECT 
    JSON_OBJECT(
        'tables', IFNULL((SELECT JSON_ARRAYAGG(JSON_OBJECT(
            'TableName', t.table_name, 
            'TableDescription', t.table_description
        )) FROM tables t), JSON_ARRAY()),
        'columns', IFNULL((SELECT JSON_ARRAYAGG(JSON_OBJECT(
            'TableName', c.table_name,
            'ColumnName', c.column_name,
            'DataType', c.data_type,
            'IsNullable', c.is_nullable,
            'CharMaxLength', c.character_maximum_length,
            'NumericPrecision', c.numeric_precision,
            'NumericScale', c.numeric_scale,
            'ColumnDescription', c.column_description
        )) FROM columns c), JSON_ARRAY()),
        'primaryKeys', IFNULL((SELECT JSON_ARRAYAGG(JSON_OBJECT(
            'TableName', pk.table_name,
            'ColumnName', pk.column_name
        )) FROM primary_keys pk), JSON_ARRAY()),
        'foreignKeys', IFNULL((SELECT JSON_ARRAYAGG(JSON_OBJECT(
            'SourceTable', fk.source_table,
            'SourceColumn', fk.source_column,
            'TargetTable', fk.target_table,
            'TargetColumn', fk.target_column
        )) FROM foreign_keys fk), JSON_ARRAY())
    ) as metadata
`

// GetMetadataQuery 获取MySQL元数据查询SQL和参数
func (my *MySQLDialect) GetMetadataQuery() (string, []interface{}) {
	return mysqlMetadataQuery, []interface{}{my.schema, my.schema, my.schema, my.schema, my.schema}
}
