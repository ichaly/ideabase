# PostgreSQL SQL 模板设计

## 命名规范

### 表别名命名

- `表名_数字`: 实际表查询的别名，如 `sys_area_0`，数字表示嵌套层级
- `__root_x`: 根查询别名，用于构建最外层 JSON 对象
- `__rcte_表名`: 递归 CTE 查询的别名

### 子查询别名命名

- `__sj_数字`: JSON Subquery 的缩写，用于 JSON 聚合操作的子查询
- `__sr_数字`: Result Set 的缩写，用于存储结果集的子查询

### 编号规则

1. 主查询从 0 开始编号
2. 关联查询按层级递增编号
3. `__sj_`和`__sr_`的编号保持一致，表示同一层级
4. 每个新的关联查询递增编号

## 通用 SQL 模板

### 基础查询结构

```sql
-- 基础结构：支持CTE、多表查询、条件过滤
WITH RECURSIVE
-- 递归CTE定义(如果需要)
"__rcte_表名" AS (
    -- 基础查询
    SELECT [字段列表]
    FROM [表名]
    WHERE [基础条件]

    UNION ALL

    -- 递归部分
    SELECT [字段列表]
    FROM [表名], "__rcte_表名"
    WHERE [递归条件]
),

-- 数据变更CTE(增删改操作)
"表名" AS (
    -- INSERT
    INSERT INTO [表名] ([字段列表])
    SELECT [值列表]
    [ON CONFLICT 处理]
    RETURNING *

    -- 或 UPDATE
    UPDATE [表名]
    SET ([字段列表]) = (SELECT [值列表])
    WHERE [条件]
    RETURNING *

    -- 或 DELETE
    DELETE FROM [表名]
    WHERE [条件]
    RETURNING *
)

-- 主查询
SELECT jsonb_build_object([查询名列表]) AS __root
FROM (SELECT true) AS __root_x

-- 关联查询部分
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
                [表别名].[字段2] AS [别名2],
                "__sj_1"."json" AS [关联1]
            FROM (
                -- 基础查询或CTE结果
                SELECT [字段列表]
                FROM [表名或CTE]
                WHERE [条件]
                [ORDER BY 子句]
                [LIMIT 限制]
                [OFFSET 偏移]
            ) AS [表别名]

            -- 一对多关联
            LEFT OUTER JOIN LATERAL (
                SELECT COALESCE(jsonb_agg(__sj_1.json), '[]') AS json
                FROM (
                    SELECT to_jsonb(__sr_1.*) AS json
                    FROM (
                        SELECT [字段列表]
                        FROM [关联表]
                        WHERE [关联条件] AND [过滤条件]
                        [ORDER BY 子句]
                        [LIMIT 限制]
                    ) AS "__sr_1"
                ) AS "__sj_1"
            ) AS "__sj_1" ON true

            -- 多对一关联
            LEFT OUTER JOIN LATERAL (
                SELECT COALESCE(jsonb_agg(__sj_2.json), '[]') AS json
                FROM (
                    SELECT to_jsonb(__sr_2.*) AS json
                    FROM (
                        SELECT [字段列表]
                        FROM [关联表]
                        WHERE [关联条件] = [外键] AND [过滤条件]
                        [LIMIT 限制]
                    ) AS "__sr_2"
                ) AS "__sj_2"
            ) AS "__sj_2" ON true

            -- 多对多关联
            LEFT OUTER JOIN LATERAL (
                SELECT COALESCE(jsonb_agg(__sj_3.json), '[]') AS json
                FROM (
                    SELECT to_jsonb(__sr_3.*) AS json
                    FROM (
                        SELECT [字段列表]
                        FROM [关联表]
                        INNER JOIN [中间表] ON ([关联条件])
                        WHERE [过滤条件]
                        [LIMIT 限制]
                    ) AS "__sr_3"
                ) AS "__sj_3"
            ) AS "__sj_3" ON true

        ) AS "__sr_0"
    ) AS "__sj_0"
) AS "__sj_0" ON true
```

## 实现说明

### 1. CTE (WITH) 的使用场景

- **递归查询**：用于处理树形结构数据，如组织架构、菜单等
- **数据变更**：在同一查询中完成增删改操作
- **复杂条件**：将复杂的条件查询分解成多个 CTE
- **性能优化**：通过 CTE 复用查询结果

### 2. LATERAL JOIN 的优势

- **相关子查询**：可以引用外层查询的字段
- **性能优化**：避免重复执行子查询
- **灵活性**：支持复杂的关联关系
- **JSON 构建**：便于构建嵌套的 JSON 结构

### 3. 参数占位符说明

- 使用 `?` 作为参数占位符
- 支持的条件类型：
  - 等值比较：`字段 = ?`
  - 范围比较：`字段 >= ?`, `字段 <= ?`
  - IN 条件：`字段 IN (?)`
  - LIKE 条件：`字段 LIKE ?`
  - NULL 检查：`字段 IS NULL`, `字段 IS NOT NULL`

### 4. 关联关系处理

- **一对多**：使用外键直接关联
- **多对一**：通过外键反向关联
- **多对多**：通过中间表关联
- **自关联**：使用递归 CTE 处理无限层级

### 5. JSON 处理策略

- **最外层**：使用`jsonb_build_object`构建
- **数组聚合**：使用`jsonb_agg`和`COALESCE`确保返回`[]`而不是`NULL`
- **对象转换**：使用`to_jsonb`将行转换为 JSON 对象
- **字段别名**：通过 AS 指定 JSON 属性名

### 6. 性能优化建议

- 合理使用索引
- 控制递归深度
- 使用 LIMIT 限制返回数据量
- 适当使用 WHERE 条件过滤
- 避免过深的嵌套查询

### 7. 使用示例

```sql
-- 示例1：带条件的一对多查询
query{
    areaList(where:{id:{eq:1}}){
        id
        name
        userList(where:{status:{eq:"active"}}){
            id
            name
        }
    }
}

-- 示例2：多对多关联查询
query{
    userList{
        id
        name
        teamList(where:{type:{eq:"project"}}){
            id
            name
        }
    }
}

-- 示例3：递归树形结构查询
query{
    menuList{
        id
        name
        children{
            id
            name
        }
    }
}
```

### 8. 注意事项

1. 命名规范要统一
2. 合理控制查询深度
3. 注意参数类型匹配
4. 关注查询性能
5. 维护适当的索引
6. 处理好 NULL 值
7. 控制返回数据量
