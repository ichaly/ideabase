# PostgreSQL 排序处理模块

## 概述

本模块提供了统一的ORDER BY子句处理功能，支持新版本的`sort`参数和旧版本的`orderBy`参数，确保向后兼容性。

## 功能特性

### 1. 统一的排序入口
- `buildOrderBy`: 统一入口方法，自动处理不同版本的排序参数
- 优先处理新版本的`sort`参数
- 兼容处理旧版本的`orderBy`参数

### 2. 新版本排序（sort参数）
- 支持PostgreSQL特有的NULL值排序
- 支持复杂的排序方向枚举
- 更灵活的字段配置

### 3. 旧版本兼容（orderBy参数）
- 保持向后兼容性
- 支持简单的ASC/DESC排序
- 平滑迁移路径

### 4. 表别名支持
- `buildOrderByWithAlias`: 支持带表别名的排序
- `buildSortFieldWithAlias`: 构建带别名的排序字段
- 适用于复杂查询和JOIN场景

## 排序方向支持

### PostgreSQL特有排序
- `ASC_NULLS_FIRST`: 升序，NULL值在前
- `DESC_NULLS_FIRST`: 降序，NULL值在前
- `ASC_NULLS_LAST`: 升序，NULL值在后
- `DESC_NULLS_LAST`: 降序，NULL值在后

### 标准排序
- `ASC`: 升序（默认）
- `DESC`: 降序

## 使用示例

### 新版本排序（推荐）
```graphql
query {
  users(sort: [
    { name: ASC_NULLS_LAST }
    { createdAt: DESC }
  ]) {
    items {
      id
      name
      createdAt
    }
  }
}
```

### 旧版本排序（兼容）
```graphql
query {
  users(orderBy: [
    { name: "ASC" }
    { createdAt: "DESC" }
  ]) {
    items {
      id
      name
      createdAt
    }
  }
}
```

## 生成的SQL示例

### 新版本
```sql
SELECT ... FROM users ORDER BY "name" ASC NULLS LAST, "created_at" DESC
```

### 旧版本
```sql
SELECT ... FROM users ORDER BY "name" ASC, "created_at" DESC
```

## 架构设计

### 方法层次结构
```
buildOrderBy (统一入口)
├── buildSortOrderBy (新版本处理)
│   ├── buildSortValue
│   └── buildSortField
├── buildLegacyOrderBy (旧版本兼容)
└── buildOrderByWithAlias (表别名支持)
    ├── buildSortValueWithAlias
    └── buildSortFieldWithAlias
```

### 设计原则
1. **向后兼容**: 保持旧版本API的完全兼容
2. **统一入口**: 通过单一方法处理所有排序需求
3. **功能分离**: 新旧版本逻辑分离，便于维护
4. **扩展性**: 易于添加新的排序功能
5. **错误处理**: 完善的错误检查和提示

## 错误处理

模块提供了完善的错误处理机制：
- 空字段名检查
- 无效排序方向检查
- 参数类型验证
- 详细的错误信息

## 性能优化

1. **最小化SQL生成**: 只在需要时生成ORDER BY子句
2. **智能参数处理**: 避免不必要的参数解析
3. **高效字符串构建**: 使用Context的优化写入方法
4. **条件短路**: 快速跳过空参数

## 扩展指南

### 添加新的排序方向
在`buildSortField`方法中添加新的case分支：

```go
case "NEW_DIRECTION":
    ctx.Write(" NEW SQL SYNTAX")
```

### 添加数据库特定功能
创建新的方法处理特定数据库的排序特性：

```go
func (my *Dialect) buildPostgreSQLSpecificSort(ctx *compiler.Context, ...) error {
    // PostgreSQL特有的排序逻辑
}
```

## 测试覆盖

建议的测试用例：
- 基础排序功能测试
- NULL值排序测试
- 多字段排序测试
- 表别名排序测试
- 错误处理测试
- 向后兼容性测试

## 注意事项

1. **参数优先级**: `sort`参数优先于`orderBy`参数
2. **默认排序**: 未指定方向时默认为ASC
3. **字段验证**: 确保排序字段在表中存在
4. **性能考虑**: 大量数据排序时注意索引优化
