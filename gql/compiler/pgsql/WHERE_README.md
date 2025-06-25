# PostgreSQL WHERE子句处理模块

## 概述

本模块提供了完善的WHERE子句处理功能，支持复杂的条件查询，包括逻辑操作符、比较操作符、NULL检查、模式匹配等。

## 功能特性

### 1. 基础比较操作符
- `eq`: 等于 (=)
- `ne`: 不等于 (!=)
- `gt`: 大于 (>)
- `ge`: 大于等于 (>=)
- `lt`: 小于 (<)
- `le`: 小于等于 (<=)

### 2. 模式匹配
- `like`: 模式匹配 (LIKE)
- `ilike`: 不区分大小写的模式匹配 (ILIKE)
- `regex`: 正则表达式匹配 (~)
- `iregex`: 不区分大小写的正则表达式匹配 (~*)

### 3. 集合操作
- `in`: 在集合中 (IN)

### 4. NULL检查
- `is`: NULL检查 (IS NULL / IS NOT NULL)

### 5. 逻辑操作符
- `and`: 逻辑与 (AND)
- `or`: 逻辑或 (OR)
- `not`: 逻辑非 (NOT)

## 使用示例

### 简单条件
```graphql
query {
  users(where: { id: { eq: 1 } }) {
    id
    name
  }
}
```

生成SQL:
```sql
SELECT "id", "name" FROM "users" WHERE "id" = $1
```

### IN条件
```graphql
query {
  users(where: { status: { in: ["active", "pending"] } }) {
    id
    name
  }
}
```

生成SQL:
```sql
SELECT "id", "name" FROM "users" WHERE "status" IN ($1, $2)
```

### AND条件
```graphql
query {
  users(where: { 
    and: [
      { age: { gt: 18 } },
      { status: { eq: "active" } }
    ]
  }) {
    id
    name
  }
}
```

生成SQL:
```sql
SELECT "id", "name" FROM "users" WHERE ("age" > $1 AND "status" = $2)
```

### OR条件
```graphql
query {
  users(where: { 
    or: [
      { type: { eq: "admin" } },
      { role: { eq: "manager" } }
    ]
  }) {
    id
    name
  }
}
```

生成SQL:
```sql
SELECT "id", "name" FROM "users" WHERE ("type" = $1 OR "role" = $2)
```

### NOT条件
```graphql
query {
  users(where: { 
    not: { deleted: { eq: true } }
  }) {
    id
    name
  }
}
```

生成SQL:
```sql
SELECT "id", "name" FROM "users" WHERE NOT ("deleted" = $1)
```

### NULL检查
```graphql
query {
  users(where: { deleted_at: { is: true } }) {
    id
    name
  }
}
```

生成SQL:
```sql
SELECT "id", "name" FROM "users" WHERE "deleted_at" IS NULL
```

### 复杂嵌套条件
```graphql
query {
  users(where: { 
    and: [
      { status: { eq: "active" } },
      { 
        or: [
          { age: { gt: 18 } },
          { role: { eq: "admin" } }
        ]
      }
    ]
  }) {
    id
    name
  }
}
```

生成SQL:
```sql
SELECT "id", "name" FROM "users" 
WHERE ("status" = $1 AND ("age" > $2 OR "role" = $3))
```

### LIKE模式匹配
```graphql
query {
  users(where: { name: { like: "%admin%" } }) {
    id
    name
  }
}
```

生成SQL:
```sql
SELECT "id", "name" FROM "users" WHERE "name" LIKE $1
```

### 正则表达式匹配
```graphql
query {
  users(where: { email: { regex: ".*@admin\\.com$" } }) {
    id
    name
  }
}
```

生成SQL:
```sql
SELECT "id", "name" FROM "users" WHERE "email" ~ $1
```

## 实现特点

1. **类型安全**: 所有参数都通过参数化查询传递，防止SQL注入
2. **灵活性**: 支持任意深度的嵌套条件
3. **性能优化**: 智能括号处理，避免不必要的括号
4. **错误处理**: 完善的错误检查和提示
5. **扩展性**: 易于添加新的操作符和功能
6. **命名规范**: 所有方法使用`build`前缀，与项目整体命名规范保持一致

## 错误处理

模块提供了完善的错误处理机制：

- 空的逻辑操作符条件检查
- 不支持的操作符检查
- 参数值验证
- 类型检查

## 测试覆盖

模块包含了全面的测试用例：

- 基础功能测试
- 高级功能测试
- 错误处理测试
- 边界条件测试

所有测试都通过SQL归一化处理，确保测试结果的一致性和可靠性。
