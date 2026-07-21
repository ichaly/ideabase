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
// 要点：
//  1. regclass转换用format('%I.%I')按标识符引用，含大写/特殊字符的表名不会解析失败；
//  2. 主键/外键改查pg_constraint：conkey/confkey按位置unnest配对，复合外键不会笛卡尔积错配，
//     且约束天然挂在本schema的表上，不会因约束重名串到其他schema；
//  3. 所有聚合均带ORDER BY，返回顺序跨启动稳定
const pgsqlMetaSQL = `
WITH
  tables AS (
    SELECT
      c.table_name,
      obj_description(format('%I.%I', c.table_schema, c.table_name)::regclass, 'pg_class') as table_description
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
      c.ordinal_position,
      c.data_type,
      c.is_nullable = 'YES' as is_nullable,
      c.character_maximum_length,
      c.numeric_precision,
      c.numeric_scale,
      col_description(format('%I.%I', c.table_schema, c.table_name)::regclass, c.ordinal_position) as column_description
    FROM
      information_schema.columns c
    WHERE
      c.table_schema = $1
  ),
  primary_keys AS (
    SELECT
      rel.relname as table_name,
      att.attname as column_name,
      con.conname as constraint_name,
      cols.ord
    FROM pg_constraint con
    JOIN pg_class rel ON rel.oid = con.conrelid
    JOIN pg_namespace nsp ON nsp.oid = rel.relnamespace
    CROSS JOIN LATERAL unnest(con.conkey) WITH ORDINALITY AS cols(attnum, ord)
    JOIN pg_attribute att ON att.attrelid = con.conrelid AND att.attnum = cols.attnum
    WHERE con.contype = 'p' AND nsp.nspname = $1
  ),
  foreign_keys AS (
    SELECT
      src.relname as source_table,
      sa.attname as source_column,
      tgt.relname as target_table,
      ta.attname as target_column,
      con.conname as constraint_name,
      cols.ord
    FROM pg_constraint con
    JOIN pg_class src ON src.oid = con.conrelid
    JOIN pg_namespace nsp ON nsp.oid = src.relnamespace
    JOIN pg_class tgt ON tgt.oid = con.confrelid
    CROSS JOIN LATERAL unnest(con.conkey, con.confkey) WITH ORDINALITY AS cols(src_attnum, tgt_attnum, ord)
    JOIN pg_attribute sa ON sa.attrelid = con.conrelid AND sa.attnum = cols.src_attnum
    JOIN pg_attribute ta ON ta.attrelid = con.confrelid AND ta.attnum = cols.tgt_attnum
    WHERE con.contype = 'f' AND nsp.nspname = $1
  )
SELECT
  json_build_object(
    'tables', (SELECT json_agg(json_build_object(
      'table_name', t.table_name,
      'table_description', t.table_description
    ) ORDER BY t.table_name) FROM tables t),
    'columns', (SELECT json_agg(json_build_object(
      'table_name', c.table_name,
      'column_name', c.column_name,
      'data_type', c.data_type,
      'is_nullable', c.is_nullable,
      'character_maximum_length', c.character_maximum_length,
      'numeric_precision', c.numeric_precision,
      'numeric_scale', c.numeric_scale,
      'column_description', c.column_description
    ) ORDER BY c.table_name, c.ordinal_position) FROM columns c),
    'primaryKeys', (SELECT json_agg(json_build_object(
      'table_name', pk.table_name,
      'column_name', pk.column_name
    ) ORDER BY pk.table_name, pk.constraint_name, pk.ord) FROM primary_keys pk),
    'foreignKeys', (SELECT json_agg(json_build_object(
      'source_table', fk.source_table,
      'source_column', fk.source_column,
      'target_table', fk.target_table,
      'target_column', fk.target_column,
      'constraint_name', fk.constraint_name
    ) ORDER BY fk.source_table, fk.constraint_name, fk.ord) FROM foreign_keys fk)
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
func (my *PgsqlLoader) Load(h protocol.Hoster) error {
	args := []interface{}{my.cfg.Schema.Schema}
	return my.loadMeta(h, pgsqlMetaSQL, args)
}
