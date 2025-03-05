package metadata

import (
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/log"
	"gorm.io/gorm"
)

// postgresVersionQuery 获取PostgreSQL版本的查询
const postgresVersionQuery = "SELECT version()"

// isPostgresVersionSupported 检查PostgreSQL版本是否为9.6或更高
func isPostgresVersionSupported(db *gorm.DB) (bool, error) {
	var versionStr string
	if err := db.Raw(postgresVersionQuery).Scan(&versionStr).Error; err != nil {
		return false, fmt.Errorf("获取PostgreSQL版本失败: %w", err)
	}

	// PostgreSQL版本字符串格式类似于: "PostgreSQL 12.4 on x86_64-pc-linux-gnu"
	if !strings.Contains(versionStr, "PostgreSQL") {
		return false, fmt.Errorf("无效的PostgreSQL版本字符串: %s", versionStr)
	}

	// 提取版本号
	parts := strings.Split(versionStr, " ")
	if len(parts) < 2 {
		return false, fmt.Errorf("无法解析PostgreSQL版本: %s", versionStr)
	}

	versionPart := parts[1]
	versionNums := strings.Split(versionPart, ".")

	// 解析主版本号
	var majorVersion float64
	if versionNums[0] == "9" && len(versionNums) > 1 {
		// 对于9.x版本，需要考虑次版本号
		majorVersion = 9.0
		if minorVersion, err := parseFloat(versionNums[1]); err == nil {
			majorVersion += minorVersion / 10
		}
	} else {
		// 对于10及以上版本，主版本号就足够了
		if mv, err := parseFloat(versionNums[0]); err == nil {
			majorVersion = mv
		}
	}

	// 9.6及以上版本都支持
	return majorVersion >= 9.6, nil
}

// parseFloat 辅助函数：将字符串转换为float64
func parseFloat(s string) (float64, error) {
	var result float64
	_, err := fmt.Sscanf(s, "%f", &result)
	return result, err
}

// PostgresDialect PostgreSQL 9.6+数据库方言
type PostgresDialect struct {
	schema string
}

// NewPostgresDialect 创建PostgreSQL方言实例
func NewPostgresDialect(db *gorm.DB, schema string) (DatabaseDialect, error) {
	isSupported, err := isPostgresVersionSupported(db)
	if err != nil {
		return nil, fmt.Errorf("PostgreSQL版本检测失败: %w", err)
	}

	if !isSupported {
		return nil, fmt.Errorf("不支持的PostgreSQL版本，需要PostgreSQL 9.6或以上版本")
	}

	log.Debug().Msg("检测到PostgreSQL 9.6+版本，使用现代方言")

	return &PostgresDialect{
		schema: schema,
	}, nil
}

// 优化的PostgreSQL元数据查询，使用单一SQL查询获取所有元数据
const postgresMetadataQuery = `
WITH 
tables AS (
    SELECT 
        c.table_name, 
        obj_description(format('%s.%s', c.table_schema, c.table_name)::regclass, 'pg_class') as table_description
    FROM 
        information_schema.tables c
    WHERE 
        c.table_schema = $1
        AND c.table_type = 'BASE TABLE'
),
columns AS (
    SELECT 
        c.table_name, 
        c.column_name, 
        c.data_type, 
        c.is_nullable = 'YES' as is_nullable,
        c.character_maximum_length,
        c.numeric_precision,
        c.numeric_scale,
        col_description(format('%s.%s', c.table_schema, c.table_name)::regclass, c.ordinal_position) as column_description
    FROM 
        information_schema.columns c
    WHERE 
        c.table_schema = $1
),
primary_keys AS (
    SELECT 
        kcu.table_name, 
        kcu.column_name 
    FROM 
        information_schema.table_constraints tc
    JOIN 
        information_schema.key_column_usage kcu ON tc.constraint_name = kcu.constraint_name
    WHERE 
        tc.constraint_type = 'PRIMARY KEY' 
        AND tc.table_schema = $1 
),
foreign_keys AS (
    SELECT 
        kcu.table_name as source_table, 
        kcu.column_name as source_column,
        ccu.table_name as target_table,
        ccu.column_name as target_column
    FROM 
        information_schema.table_constraints tc
    JOIN 
        information_schema.key_column_usage kcu ON tc.constraint_name = kcu.constraint_name
    JOIN 
        information_schema.constraint_column_usage ccu ON tc.constraint_name = ccu.constraint_name
    WHERE 
        tc.constraint_type = 'FOREIGN KEY' 
        AND tc.table_schema = $1 
)
SELECT 
    json_build_object(
        'tables', (SELECT json_agg(json_build_object(
            'table_name', t.table_name,
            'table_description', t.table_description
        )) FROM tables t),
        'columns', (SELECT json_agg(json_build_object(
            'table_name', c.table_name,
            'column_name', c.column_name,
            'data_type', c.data_type,
            'is_nullable', c.is_nullable,
            'character_maximum_length', c.character_maximum_length,
            'numeric_precision', c.numeric_precision,
            'numeric_scale', c.numeric_scale,
            'column_description', c.column_description
        )) FROM columns c),
        'primaryKeys', (SELECT json_agg(json_build_object(
            'table_name', pk.table_name,
            'column_name', pk.column_name
        )) FROM primary_keys pk),
        'foreignKeys', (SELECT json_agg(json_build_object(
            'source_table', fk.source_table,
            'source_column', fk.source_column,
            'target_table', fk.target_table,
            'target_column', fk.target_column
        )) FROM foreign_keys fk)
    ) as metadata
`

// GetMetadataQuery 获取PostgreSQL元数据查询SQL和参数
func (my *PostgresDialect) GetMetadataQuery() (string, []interface{}) {
	return postgresMetadataQuery, []interface{}{my.schema}
}
