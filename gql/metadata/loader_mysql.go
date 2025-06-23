package metadata

import (
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/protocol"
	"gorm.io/gorm"
)

// MysqlLoader MySQL元数据加载器，实现Loader接口
type MysqlLoader struct {
	*baseLoader
}

// MySQL元数据查询SQL，返回所有表、字段、主键、外键信息
const mysqlMetaSQL = `
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
      c.table_name,
      c.column_name,
      c.data_type,
      c.is_nullable = 'YES' as is_nullable,
      c.character_maximum_length,
      c.numeric_precision,
      c.numeric_scale,
      c.column_comment as column_description
    FROM 
      information_schema.columns c
    JOIN 
      tables t ON c.table_name = t.table_name
    WHERE 
      c.table_schema = ?
  ),
  primary_keys AS (
    SELECT DISTINCT
      k.table_name,
      k.column_name
    FROM 
      information_schema.table_constraints t
    JOIN 
      information_schema.key_column_usage k 
      ON t.constraint_name = k.constraint_name
      AND t.table_schema = k.table_schema
    JOIN 
      tables tab ON k.table_name = tab.table_name
    WHERE 
      t.constraint_type = 'PRIMARY KEY'
      AND t.table_schema = ?
  ),
  foreign_keys AS (
    SELECT DISTINCT
      k.table_name as source_table,
      k.column_name as source_column,
      k.referenced_table_name as target_table,
      k.referenced_column_name as target_column
    FROM 
      information_schema.key_column_usage k
    JOIN 
      tables t1 ON k.table_name = t1.table_name
    JOIN 
      tables t2 ON k.referenced_table_name = t2.table_name
    WHERE 
      k.constraint_schema = ?
      AND k.referenced_table_name IS NOT NULL
  )
SELECT 
  JSON_OBJECT(
    'tables', IFNULL((SELECT JSON_ARRAYAGG(JSON_OBJECT(
      'table_name', t.table_name, 
      'table_description', t.table_description
    )) FROM tables t), JSON_ARRAY()),
    'columns', IFNULL((SELECT JSON_ARRAYAGG(JSON_OBJECT(
      'table_name', c.table_name,
      'column_name', c.column_name,
      'data_type', c.data_type,
      'is_nullable', c.is_nullable,
      'character_maximum_length', c.character_maximum_length,
      'numeric_precision', c.numeric_precision,
      'numeric_scale', c.numeric_scale,
      'column_description', c.column_description
    )) FROM columns c), JSON_ARRAY()),
    'primaryKeys', IFNULL((SELECT JSON_ARRAYAGG(JSON_OBJECT(
      'table_name', pk.table_name,
      'column_name', pk.column_name
    )) FROM primary_keys pk), JSON_ARRAY()),
    'foreignKeys', IFNULL((SELECT JSON_ARRAYAGG(JSON_OBJECT(
      'source_table', fk.source_table,
      'source_column', fk.source_column,
      'target_table', fk.target_table,
      'target_column', fk.target_column
    )) FROM foreign_keys fk), JSON_ARRAY())
  ) as metadata
`

// NewMysqlLoader 创建MySQL加载器
func NewMysqlLoader(cfg *internal.Config, db *gorm.DB) *MysqlLoader {
	return &MysqlLoader{
		&baseLoader{db: db, cfg: cfg},
	}
}

func (my *MysqlLoader) Name() string  { return LoaderMysql }
func (my *MysqlLoader) Priority() int { return 60 }

// Support 判断是否为MySQL数据库
func (my *MysqlLoader) Support() bool {
	return my.cfg != nil && my.cfg.IsDebug() && my.db != nil && my.db.Dialector.Name() == "mysql"
}

// Load 从MySQL加载元数据
func (my *MysqlLoader) Load(t protocol.Tree) error {
	args := []interface{}{
		my.cfg.Schema.Schema,
		my.cfg.Schema.Schema,
		my.cfg.Schema.Schema,
		my.cfg.Schema.Schema,
	}
	return my.loadMeta(t, mysqlMetaSQL, args)
}
