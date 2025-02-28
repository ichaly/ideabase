# 数据库兼容性说明文档

本文档详细说明IdeaBase项目对数据库版本的要求及其原因。

## 总体要求

IdeaBase使用现代数据库特性来优化元数据查询和处理性能，因此对所支持的数据库有明确的版本要求。目前支持的数据库系统包括：

| 数据库系统   | 最低版本要求 | 推荐版本     |
|------------|------------|------------|
| PostgreSQL | 9.6+       | 12.0或更高   |
| MySQL      | 8.0+       | 8.0.25或更高 |

## PostgreSQL兼容性

### 版本要求：PostgreSQL 9.6+

PostgreSQL 9.6版本引入了多项重要特性，这些特性是IdeaBase高效元数据查询的基础：

1. **增强的JSON支持**
   - `json_agg`函数：将多行聚合为单个JSON数组
   - `json_build_object`函数：从键值对构建JSON对象

2. **并行查询处理**
   - 提高了复杂CTE(Common Table Expression)查询的性能

### PostgreSQL关键功能使用

IdeaBase中，我们使用以下PostgreSQL特性：

```sql
-- 使用WITH子句(CTE)和json聚合函数
WITH 
tables AS (...),
columns AS (...),
primary_keys AS (...),
foreign_keys AS (...)
SELECT 
    json_build_object(
        'tables', (SELECT json_agg(t) FROM tables t),
        'columns', (SELECT json_agg(c) FROM columns c),
        ...
    ) as metadata
```

## MySQL兼容性

### 版本要求：MySQL 8.0+

MySQL 8.0版本引入了关键特性，使其能够与PostgreSQL在功能上对齐：

1. **公用表表达式（CTE）**
   - 支持`WITH`子句，允许在单一查询中定义多个临时结果集

2. **JSON函数增强**
   - `JSON_OBJECT`：创建JSON对象
   - `JSON_ARRAYAGG`：将多行聚合为JSON数组

### MySQL关键功能使用

IdeaBase中，我们使用以下MySQL特性：

```sql
-- 使用WITH子句(CTE)和JSON函数
WITH 
tables AS (...),
columns AS (...),
primary_keys AS (...),
foreign_keys AS (...)
SELECT 
    JSON_OBJECT(
        'tables', IFNULL((SELECT JSON_ARRAYAGG(JSON_OBJECT(...)) FROM tables t), JSON_ARRAY()),
        ...
    ) as metadata
```

## 版本检测机制

IdeaBase在初始化时会检测数据库版本，具体实现：

1. **MySQL版本检测**
   ```go
   func isMySQLVersionSupported(db *gorm.DB) error {
       var version string
       // 检查版本是否为8.0+
       // 若不支持，返回详细错误信息
   }
   ```

2. **PostgreSQL版本检测**
   ```go
   func isPostgresVersionSupported(db *gorm.DB) error {
       var versionStr string
       // 检查版本是否为9.6+
       // 若不支持，返回详细错误信息
   }
   ```

## 错误处理

当检测到不支持的数据库版本时，系统将返回明确的错误信息：

- MySQL低版本：`"不支持的MySQL版本: %s，需要MySQL 8.0或以上版本"`
- PostgreSQL低版本：`"不支持的PostgreSQL版本: %s，需要PostgreSQL 9.6或以上版本"`

## 未来扩展计划

未来版本中，我们计划：

1. 为MariaDB添加专门的支持，兼容MariaDB 10.2+版本
2. 考虑为SQLite添加有限的支持
3. 探索Oracle和SQL Server的兼容性方案

---

如有疑问或需求，请提交Issue或PR。 