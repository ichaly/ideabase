package metadata

import (
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/protocol"
	"gorm.io/gorm"
)

// PgsqlLoader PostgreSQL元数据加载器，实现Loader接口
type PgsqlLoader struct {
	*baseLoader
}

// PostgreSQL元数据查询SQL，返回所有表、字段、主键、外键信息
const pgsqlMetaSQL = `
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

// NewPgsqlLoader 创建PostgreSQL加载器
func NewPgsqlLoader(cfg *internal.Config, db *gorm.DB) *PgsqlLoader {
	return &PgsqlLoader{
		&baseLoader{db: db, cfg: cfg},
	}
}

func (my *PgsqlLoader) Name() string  { return LoaderPgsql }
func (my *PgsqlLoader) Priority() int { return 60 }
func (my *PgsqlLoader) Support() bool {
	return my.cfg != nil && my.cfg.IsDebug() && my.db != nil && my.db.Dialector.Name() == "postgres"
}

// Load 从PostgreSQL加载元数据
func (my *PgsqlLoader) Load(t protocol.Tree) error {
	args := []interface{}{my.cfg.Schema.Schema}
	return my.loadMeta(t, pgsqlMetaSQL, args)
}
