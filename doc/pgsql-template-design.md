# PostgreSQL SQL 模板设计

## 命名规范

### 表别名命名

- `表名_数字`: 实际表查询的别名，数字表示嵌套层级，如 `sys_area_0`
- `__root_x`: 根查询别名
- `__rcte_表名`: 递归 CTE 查询的别名
- `__sj_数字`: JSON 子查询别名(Subquery JSON)
- `__sr_数字`: 结果集别名(Subquery Result)

### 编号规则

1. 主查询从 0 开始编号
2. 关联查询按层级递增编号
3. `__sj_`和`__sr_`的编号保持一致，表示同一层级
4. 每个新的关联查询递增编号

## 通用 SQL 模板

```sql
-- 完整的通用SQL模板
WITH RECURSIVE
-- [可选] 递归CTE定义
"__rcte_表名" AS (
    -- 基础查询
    SELECT [字段列表]
    FROM [表名]
    WHERE [表名].id = [起始ID]
    LIMIT 1

    UNION ALL

    -- 递归部分，支持以下几种模式：
    -- 1. 简单递归
    SELECT [字段列表]
    FROM [表名], "__rcte_表名"
    WHERE [表名].[pid/id] = "__rcte_表名".[id/pid]

    -- 2. 安全递归（防止循环）
    SELECT [字段列表]
    FROM [表名], "__rcte_表名"
    WHERE (
        [表名].[pid/id] = "__rcte_表名".[id/pid] AND
        [表名].[pid] IS NOT NULL AND
        [表名].[pid] != [表名].[id]
    )

    -- 3. 条件递归
    SELECT [字段列表]
    FROM [表名], "__rcte_表名"
    WHERE (
        [表名].[pid/id] = "__rcte_表名".[id/pid] AND
        [其他过滤条件]
    )
),

-- [可选] 数据变更CTE
"表名" AS (
    -- INSERT
    INSERT INTO [表名] ([字段列表])
    SELECT
        [值]::[类型], -- 例如 '重庆'::character varying
        ...
    [ON CONFLICT DO UPDATE SET [字段] = EXCLUDED.[字段]]
    [ON CONFLICT DO NOTHING]
    RETURNING *

    -- 或 UPDATE
    UPDATE [表名]
    SET
        [字段] = [值]::[类型],
        [字段] = [表达式],
        ([字段列表]) = (SELECT [值列表])
    WHERE [条件]
    RETURNING *

    -- 或 DELETE
    DELETE FROM [表名]
    WHERE [条件]
    RETURNING *
),

-- 主查询
SELECT jsonb_build_object(
    '查询名1', __sj_0.json
    [,'查询名2', __sj_1.json]  -- 多个查询时添加
    ...
) AS __root
FROM (SELECT true) AS __root_x

-- 查询体
LEFT OUTER JOIN LATERAL (
    -- JSON数组聚合
    SELECT COALESCE(jsonb_agg(
        CASE WHEN [去重条件]
        THEN DISTINCT ON ([去重字段]) __sj_0.json
        ELSE __sj_0.json END
    ), '[]') AS json
    FROM (
        -- 转换为JSON对象
        SELECT to_jsonb(__sr_0.*) AS json
        FROM (
            -- 字段选择和关联
            SELECT
                [表别名].[字段1]::[类型] AS [别名1],
                [表别名].[字段2]::[类型] AS [别名2]
                [,"__sj_1"."json" AS [关联字段]]  -- 有关联查询时添加
            FROM (
                -- 基础查询
                SELECT [字段列表]
                FROM [表名或CTE]
                [WHERE (  -- 条件子句，支持多种组合
                    -- 1. 基础条件
                    ([字段] [操作符] [值]::[类型]) OR
                    ([字段] [操作符] [值])

                    -- 2. 复合条件
                    AND/OR (
                        [条件1] AND/OR [条件2]
                    )

                    -- 3. 关联条件
                    AND [外键] = [主键]

                    -- 4. 空值条件
                    AND [字段] IS [NOT] NULL

                    -- 5. 范围条件
                    AND [字段] BETWEEN [值1] AND [值2]

                    -- 6. IN条件
                    AND [字段] IN ([值列表])

                    -- 7. 模糊匹配
                    AND [字段] LIKE [模式]
                )]
                [GROUP BY [字段列表]]
                [HAVING [条件]]
                [ORDER BY
                    [字段] [ASC|DESC] [NULLS FIRST|NULLS LAST],
                    ...
                ]
                [LIMIT ?]
                [OFFSET ?]
            ) AS [表别名]

            -- [可选] 一对多关联
            LEFT OUTER JOIN LATERAL (
                SELECT COALESCE(jsonb_agg(__sj_N.json), '[]') AS json
                FROM (
                    SELECT to_jsonb(__sr_N.*) AS json
                    FROM (
                        SELECT [字段列表]
                        FROM [关联表]
                        WHERE [外键] = [主键] AND [其他条件]
                        [ORDER BY 子句]
                        [LIMIT ?]
                    ) AS "__sr_N"
                ) AS "__sj_N"
            ) AS "__sj_N" ON true

            -- [可选] 多对一关联
            LEFT OUTER JOIN LATERAL (
                SELECT COALESCE(jsonb_agg(__sj_N.json), '[]') AS json
                FROM (
                    SELECT to_jsonb(__sr_N.*) AS json
                    FROM (
                        SELECT [字段列表]
                        FROM [关联表]
                        WHERE [主键] = [外键] AND [其他条件]
                        [LIMIT ?]
                    ) AS "__sr_N"
                ) AS "__sj_N"
            ) AS "__sj_N" ON true

            -- [可选] 多对多关联
            LEFT OUTER JOIN LATERAL (
                SELECT COALESCE(jsonb_agg(__sj_N.json), '[]') AS json
                FROM (
                    SELECT to_jsonb(__sr_N.*) AS json
                    FROM (
                        SELECT [字段列表]
                        FROM [关联表]
                        INNER JOIN [中间表] ON (
                            [中间表].[外键1] = [主表].[主键] AND
                            [中间表].[外键2] = [关联表].[主键]
                        )
                        WHERE [其他条件]
                        [LIMIT ?]
                    ) AS "__sr_N"
                ) AS "__sj_N"
            ) AS "__sj_N" ON true

        ) AS "__sr_0"
    ) AS "__sj_0"
) AS "__sj_0" ON true
```

## 使用说明

1. 模板组成部分：

   - WITH RECURSIVE 子句（可选）
   - 递归 CTE 定义（树形结构查询时使用）
   - 数据变更 CTE（增删改操作时使用）
   - 主查询结构（必需）
   - 查询体（必需）
   - 关联查询（按需添加）

2. 使用方法：

   - 基础查询：仅保留主查询和基本查询体
   - 关联查询：添加需要的关联部分
   - 树形结构：添加递归 CTE 定义
   - 数据变更：添加相应的变更 CTE

3. 条件组合：

   - 基础条件：字段比较
   - AND/OR 组合
   - NOT 条件
   - 关联条件
   - 排序和分页
   - 类型转换
   - NULL 值处理

4. 特殊处理：
   - NULL 值使用 COALESCE 确保返回 `[]`
   - 去重使用 DISTINCT ON
   - 递归查询注意限制深度
   - 关联查询控制数据量
   - 类型转换使用 `::`
   - 多表关联使用适当的 JOIN 类型
   - 条件组合注意优先级

## 注意事项

1. 命名规范要统一
2. 合理控制查询深度
3. 注意参数类型匹配
4. 关注查询性能
5. 维护适当的索引
6. 处理好 NULL 值
7. 控制返回数据量
8. 考虑并发场景
9. 注意类型转换
10. 关注数据一致性
