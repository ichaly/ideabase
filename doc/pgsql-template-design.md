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
-- [可选] 递归CTE定义，用于树形结构查询
"__rcte_表名" AS (
    -- 基础查询
    SELECT [字段列表]
    FROM [表名]
    WHERE [表名].id = [起始ID]
    LIMIT 1

    UNION ALL

    -- 递归部分(根据场景二选一)
    -- 向上递归(查父节点)
    SELECT child.[字段列表]
    FROM [表名] child, "__rcte_表名" parent
    WHERE (
        child.id = parent.pid AND
        parent.pid IS NOT NULL AND
        parent.pid != parent.id
        [AND 其他过滤条件]
    )
    -- 或 向下递归(查子节点)
    SELECT parent.[字段列表]
    FROM [表名] parent, "__rcte_表名" child
    WHERE (
        parent.pid = child.id AND
        parent.pid IS NOT NULL AND
        parent.pid != parent.id
        [AND 其他过滤条件]
    )
),

-- [可选] 数据变更CTE，用于增删改操作
"表名" AS (
    -- INSERT
    INSERT INTO [表名] ([字段列表])
    SELECT [值列表]
    [ON CONFLICT 处理]
    RETURNING *

    -- 或 UPDATE
    UPDATE [表名]
    SET [字段] = [值], ...
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
    SELECT COALESCE(jsonb_agg(__sj_0.json), '[]') AS json
    FROM (
        -- 转换为JSON对象
        SELECT to_jsonb(__sr_0.*) AS json
        FROM (
            -- 字段选择和关联
            SELECT
                [表别名].[字段1] AS [别名1],
                [表别名].[字段2] AS [别名2]
                [,"__sj_1"."json" AS [关联字段]]  -- 有关联查询时添加
            FROM (
                -- 基础查询
                SELECT [字段列表]
                FROM [表名或CTE]
                [WHERE (  -- 条件子句，根据需要组合
                    ([字段] [操作符] [值])
                    [AND/OR (其他条件)]
                    [AND [外键] = [主键]]  -- 关联条件
                )]
                [GROUP BY [字段列表]]
                [HAVING [条件]]
                [ORDER BY [字段] [ASC|DESC] [NULLS FIRST|NULLS LAST]]
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
                        INNER JOIN [中间表] ON ([关联条件])
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

4. 特殊处理：
   - NULL 值使用 COALESCE 确保返回 `[]`
   - 去重使用 DISTINCT ON
   - 递归查询注意限制深度
   - 关联查询控制数据量

## 注意事项

1. 命名规范要统一
2. 合理控制查询深度
3. 注意参数类型匹配
4. 关注查询性能
5. 维护适当的索引
6. 处理好 NULL 值
7. 控制返回数据量
8. 考虑并发场景
