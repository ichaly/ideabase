-- pgsql一条sql实现查询所有schema为public的表名,列名,类型,是否为空,是否主键,是否外键,外键表名,外键字段,表注释,列注释信息,且在有多对多中间表是只有一条中间表记录
SELECT t.table_name,
       c.column_name,
       c.data_type,
       CAST(c.is_nullable AS BOOLEAN) AS is_nullable,
       COALESCE(pk.is_primary, false) AS is_primary,
       COALESCE(fk.is_foreign, false) AS is_foreign,
       fk.table_relation,
       fk.column_relation,
       pgd.description                AS table_description,
       pd.description                 AS column_description
FROM information_schema.tables t
         JOIN
     information_schema.columns c ON t.table_schema = c.table_schema AND t.table_name = c.table_name
         LEFT JOIN LATERAL (
    SELECT true AS is_primary
    FROM information_schema.table_constraints tc
             JOIN
         information_schema.key_column_usage kcu ON tc.constraint_name = kcu.constraint_name
    WHERE tc.constraint_type = 'PRIMARY KEY'
      AND tc.table_schema = t.table_schema
      AND tc.table_name = t.table_name
      AND kcu.column_name = c.column_name
    ) pk ON true
         LEFT JOIN LATERAL (
    SELECT true            AS is_foreign,
           ccu.table_name  AS table_relation,
           ccu.column_name AS column_relation
    FROM information_schema.table_constraints tc
             JOIN
         information_schema.key_column_usage kcu ON tc.constraint_name = kcu.constraint_name
             JOIN
         information_schema.constraint_column_usage ccu ON ccu.constraint_name = tc.constraint_name
    WHERE tc.constraint_type = 'FOREIGN KEY'
      AND tc.table_schema = t.table_schema
      AND tc.table_name = t.table_name
      AND kcu.column_name = c.column_name
    ) fk ON true
         LEFT JOIN
     pg_catalog.pg_description pgd ON pgd.objoid = (SELECT oid FROM pg_catalog.pg_class WHERE relname = t.table_name)
         AND pgd.objsubid = 0
         LEFT JOIN
     pg_catalog.pg_description pd ON pd.objoid = (SELECT oid FROM pg_catalog.pg_class WHERE relname = t.table_name)
         AND pd.objsubid = c.ordinal_position
WHERE t.table_schema = 'public'
ORDER BY t.table_name, c.ordinal_position;

