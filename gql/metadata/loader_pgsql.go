package metadata

import (
	sqldb "github.com/ichaly/ideabase/gql/database"
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/protocol"
)

// pgsqlMetaSQL reads one schema through pg_catalog so every supported relkind,
// quoted identifier, type, and positional constraint uses the same source.
const pgsqlMetaSQL = `
WITH relations AS (
  SELECT
    c.oid AS relation_id,
    n.nspname AS schema,
    c.relname AS table_name,
    obj_description(c.oid, 'pg_class') AS table_description,
    CASE c.relkind
      WHEN 'r' THEN 'table'
      WHEN 'p' THEN 'partitioned'
      WHEN 'v' THEN 'view'
      WHEN 'm' THEN 'materialized'
      WHEN 'f' THEN 'foreign'
    END AS table_kind
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE n.nspname = $1 AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
), columns AS (
  SELECT
    r.table_name,
    a.attname AS column_name,
    a.attnum AS ordinal,
    COALESCE(base.typname, t.typname) AS data_type,
    NOT a.attnotnull AS is_nullable,
    NULL::bigint AS character_maximum_length,
    NULL::bigint AS numeric_precision,
    NULL::bigint AS numeric_scale,
    col_description(r.relation_id, a.attnum) AS column_description,
    CASE WHEN a.attidentity = '' AND a.attgenerated = ''
      THEN COALESCE(pg_get_expr(d.adbin, d.adrelid), '') ELSE '' END AS default_expression,
    CASE
      WHEN a.attidentity = 'a' THEN 'identity-always'
      WHEN a.attidentity = 'd' THEN 'identity-by-default'
      WHEN a.attgenerated <> '' THEN 'stored'
      ELSE ''
    END AS generation_kind,
    CASE WHEN a.attgenerated <> '' THEN COALESCE(pg_get_expr(d.adbin, d.adrelid), '') ELSE '' END AS generation_expression,
    tn.nspname AS type_schema,
    t.typname AS type_name,
    CASE
      WHEN t.typcategory = 'A' THEN 'array'
      WHEN t.typtype = 'd' THEN 'domain'
      WHEN t.typtype = 'e' THEN 'enum'
      ELSE 'base'
    END AS type_kind,
    COALESCE(base.typname, '') AS base_type,
    COALESCE(en.nspname, '') AS element_schema,
    COALESCE(elem.typname, '') AS element_name,
    CASE
      WHEN elem.typtype = 'd' THEN 'domain'
      WHEN elem.typtype = 'e' THEN 'enum'
      WHEN elem.oid IS NOT NULL THEN 'base'
      ELSE ''
    END AS element_kind,
    COALESCE(elem_base.typname, '') AS element_base,
    COALESCE((
      SELECT array_agg(e.enumlabel ORDER BY e.enumsortorder)
      FROM pg_enum e
      WHERE e.enumtypid = CASE WHEN t.typcategory = 'A' THEN elem.oid ELSE t.oid END
    ), ARRAY[]::text[]) AS enum_labels
  FROM relations r
  JOIN pg_attribute a ON a.attrelid = r.relation_id AND a.attnum > 0 AND NOT a.attisdropped
  JOIN pg_type t ON t.oid = a.atttypid
  JOIN pg_namespace tn ON tn.oid = t.typnamespace
  LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
  LEFT JOIN pg_type base ON base.oid = t.typbasetype
  LEFT JOIN pg_type elem ON elem.oid = t.typelem AND t.typcategory = 'A'
  LEFT JOIN pg_namespace en ON en.oid = elem.typnamespace
  LEFT JOIN pg_type elem_base ON elem_base.oid = elem.typbasetype
), constraints AS (
  SELECT
    rel.relname AS table_name,
    con.conname AS name,
    CASE con.contype
      WHEN 'p' THEN 'primary'
      WHEN 'u' THEN 'unique'
      WHEN 'f' THEN 'foreign'
      WHEN 'c' THEN 'check'
      WHEN 'x' THEN 'exclusion'
    END AS kind,
    COALESCE((
      SELECT array_agg(a.attname ORDER BY key.ord)
      FROM unnest(con.conkey) WITH ORDINALITY AS key(attnum, ord)
      JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = key.attnum
    ), ARRAY[]::text[]) AS columns,
    COALESCE(ref.relname, '') AS referenced_table,
    COALESCE((
      SELECT array_agg(a.attname ORDER BY key.ord)
      FROM unnest(con.confkey) WITH ORDINALITY AS key(attnum, ord)
      JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = key.attnum
    ), ARRAY[]::text[]) AS referenced_columns,
    CASE WHEN con.contype IN ('c', 'x') THEN pg_get_constraintdef(con.oid, true) ELSE '' END AS expression,
    con.condeferrable AS deferrable,
    con.convalidated AS validated,
    CASE con.confupdtype WHEN 'c' THEN 'cascade' WHEN 'n' THEN 'set-null' WHEN 'd' THEN 'set-default' WHEN 'r' THEN 'restrict' WHEN 'a' THEN 'no-action' ELSE '' END AS on_update,
    CASE con.confdeltype WHEN 'c' THEN 'cascade' WHEN 'n' THEN 'set-null' WHEN 'd' THEN 'set-default' WHEN 'r' THEN 'restrict' WHEN 'a' THEN 'no-action' ELSE '' END AS on_delete,
    CASE con.confmatchtype WHEN 'f' THEN 'full' WHEN 'p' THEN 'partial' WHEN 's' THEN 'simple' ELSE '' END AS match
  FROM pg_constraint con
  JOIN pg_class rel ON rel.oid = con.conrelid
  JOIN pg_namespace n ON n.oid = rel.relnamespace
  LEFT JOIN pg_class ref ON ref.oid = con.confrelid
  WHERE n.nspname = $1 AND con.contype IN ('p', 'u', 'f', 'c', 'x')
), indexes AS (
  SELECT
    rel.relname AS table_name,
    idx.relname AS name,
    COALESCE((
      SELECT jsonb_agg(jsonb_build_object(
        'column', CASE WHEN i.indkey[pos] > 0 THEN a.attname ELSE '' END,
        'expression', CASE WHEN i.indkey[pos] = 0 THEN pg_get_indexdef(i.indexrelid, pos + 1, true) ELSE '' END
      ) ORDER BY pos)
      FROM generate_subscripts(i.indkey, 1) pos
      LEFT JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = i.indkey[pos]
      WHERE pos < i.indnkeyatts
    ), '[]'::jsonb) AS keys,
    i.indisunique AS unique,
    i.indisprimary AS primary,
    (NOT i.indisvalid OR NOT i.indisready OR NOT i.indislive) AS invalid,
    NOT i.indimmediate AS deferred,
    COALESCE(pg_get_expr(i.indpred, i.indrelid), '') AS predicate,
    am.amname AS method,
    COALESCE((
      SELECT array_agg(a.attname ORDER BY pos)
      FROM generate_subscripts(i.indkey, 1) pos
      JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = i.indkey[pos]
      WHERE pos >= i.indnkeyatts
    ), ARRAY[]::text[]) AS include
  FROM pg_index i
  JOIN pg_class rel ON rel.oid = i.indrelid
  JOIN pg_class idx ON idx.oid = i.indexrelid
  JOIN pg_namespace n ON n.oid = rel.relnamespace
  JOIN pg_am am ON am.oid = idx.relam
  WHERE n.nspname = $1
), routines AS (
  SELECT
    n.nspname AS schema,
    p.proname AS name,
    CASE p.prokind WHEN 'p' THEN 'procedure' ELSE 'function' END AS kind,
    pg_get_function_identity_arguments(p.oid) AS identity_arguments,
    COALESCE((
      SELECT jsonb_agg(jsonb_build_object(
        'name', COALESCE(p.proargnames[arg.ordinality], ''),
        'mode', CASE arg.mode WHEN 'o' THEN 'out' WHEN 'b' THEN 'inout' WHEN 'v' THEN 'variadic' WHEN 't' THEN 'table' ELSE 'in' END,
        'type', format_type(arg.type_oid, NULL),
        'default', CASE WHEN arg.mode NOT IN ('o', 't') AND arg.input_position > p.pronargs - p.pronargdefaults
          THEN pg_get_function_arg_default(p.oid, arg.input_position) ELSE '' END
      ) ORDER BY arg.ordinality)
      FROM (
        SELECT raw.*, SUM(CASE WHEN raw.mode IN ('o', 't') THEN 0 ELSE 1 END)
          OVER (ORDER BY raw.ordinality)::integer AS input_position
        FROM (
          SELECT type_oid, ordinality,
            COALESCE(p.proargmodes[ordinality], 'i') AS mode
          FROM unnest(COALESCE(p.proallargtypes, p.proargtypes::oid[]))
            WITH ORDINALITY AS types(type_oid, ordinality)
        ) raw
      ) arg
    ), '[]'::jsonb) AS arguments,
    CASE WHEN p.prokind = 'p' THEN '' ELSE regexp_replace(pg_get_function_result(p.oid), '^SETOF ', '') END AS return_type,
    p.proretset AS returns_set,
    CASE p.provolatile WHEN 'i' THEN 'immutable' WHEN 's' THEN 'stable' ELSE 'volatile' END AS volatility,
    p.prosecdef AS security_definer
  FROM pg_proc p
  JOIN pg_namespace n ON n.oid = p.pronamespace
  WHERE n.nspname = $1 AND p.prokind IN ('f', 'p')
)
SELECT json_build_object(
  'tables', (SELECT json_agg(to_jsonb(r) ORDER BY r.table_name) FROM relations r),
  'columns', (SELECT json_agg(to_jsonb(c) ORDER BY c.table_name, c.ordinal) FROM columns c),
  'primaryKeys', '[]'::json,
  'foreignKeys', '[]'::json,
  'constraints', (SELECT json_agg(to_jsonb(c) ORDER BY c.table_name, c.name) FROM constraints c),
  'indexes', (SELECT json_agg(to_jsonb(i) ORDER BY i.table_name, i.name) FROM indexes i),
  'routines', (SELECT json_agg(to_jsonb(fn) ORDER BY fn.name, fn.identity_arguments) FROM routines fn)
) AS metadata`

type PgsqlLoader struct{ *baseLoader }

func NewPgsqlLoader(cfg *internal.Config, db *sqldb.Connection) *PgsqlLoader {
	return &PgsqlLoader{&baseLoader{db: db, cfg: cfg}}
}

func (my *PgsqlLoader) Name() string  { return LoaderPgsql }
func (my *PgsqlLoader) Priority() int { return 60 }
func (my *PgsqlLoader) Support() bool {
	return my.cfg != nil && my.db != nil && my.db.Driver() == "postgres"
}
func (my *PgsqlLoader) Load(h protocol.Hoster) error {
	return my.loadMeta(h, pgsqlMetaSQL, []interface{}{my.cfg.Schema.Schema})
}
