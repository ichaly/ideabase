# IdeaBase GraphQL Schema 设计指南

## 📚 概述

IdeaBase GraphQL Schema 设计遵循以下核心原则：
- **简洁性**：使用简短而有意义的命名，减少冗余
- **灵活性**：提供丰富的查询、过滤和操作能力
- **高性能**：优化的查询结构和执行机制
- **可扩展性**：模块化结构，易于扩展

该Schema提供了完整的CRUD操作支持，包括复杂的过滤、排序、分页和聚合机制，特别适合构建高性能的数据密集型应用。

## 🔍 核心功能

### 1. 数据查询与过滤

提供了强大的过滤系统，支持：
- 精确匹配、范围查询、模糊搜索
- 复杂的布尔逻辑（AND、OR、NOT）
- 嵌套过滤（关联关系查询）
- NULL值处理

### 2. 排序机制

灵活的排序系统，支持：
- 多字段排序
- 升序/降序
- NULL值排序控制（NULL在前/NULL在后）

### 3. 分页策略

统一的分页接口，同时支持：
- 传统分页（limit/offset）
- 游标分页（cursor-based）
- 分页元数据（总数、游标信息）

### 4. 聚合函数

内置丰富的数据聚合功能：
- 计数、求和、平均值、最大/最小值
- 分组统计
- 时间序列数据聚合
- 条件聚合

## 📋 详细功能说明

### 标量类型

```graphql
scalar JSON       # JSON数据
scalar Cursor     # 游标
scalar DateTime   # 日期时间
```

### 过滤器类型

每种数据类型都有对应的过滤器，支持多种操作符：

```graphql
input StringFilter {
  eq: String        # 等于
  ne: String        # 不等于
  gt: String        # 大于
  ge: String        # 大于等于
  lt: String        # 小于
  le: String        # 小于等于
  in: [String!]     # 在列表中
  ni: [String!]     # 不在列表中
  like: String      # 模糊匹配(区分大小写)
  ilike: String     # 模糊匹配(不区分大小写)
  regex: String     # 正则表达式匹配
  iregex: String    # 正则表达式匹配(不区分大小写)
  is: IsInput       # 是否为NULL
}
```

类似的还有`IntFilter`、`FloatFilter`、`DateTimeFilter`、`BoolFilter`、`IDFilter`和`JSONFilter`。

### 实体过滤

支持复杂的实体过滤条件，包括嵌套过滤和布尔逻辑：

```graphql
input UserFilter {
  id: IDFilter
  name: StringFilter
  email: StringFilter
  role: StringFilter
  createdAt: DateTimeFilter
  updatedAt: DateTimeFilter
  posts: PostFilter           # 嵌套关联过滤
  AND: [UserFilter!]          # 逻辑与
  OR: [UserFilter!]           # 逻辑或
  NOT: UserFilter             # 逻辑非
}
```

### 统一分页

同时支持传统分页和游标分页的统一接口：

```graphql
type UserPage {
  items: [User!]!             # 直接返回User对象数组
  pageInfo: PageInfo!         # 分页元数据
  total: Int!                 # 总记录数
}

type PageInfo {
  hasNext: Boolean!           # 是否有下一页
  hasPrev: Boolean!           # 是否有上一页
  start: Cursor               # 当前页第一条记录的游标
  end: Cursor                 # 当前页最后一条记录的游标
}
```

### 数据聚合

支持各类数据聚合操作：

```graphql
type NumStats {
  sum: Float                  # 总和
  avg: Float                  # 平均值
  min: Float                  # 最小值
  max: Float                  # 最大值
  count: Int!                 # 计数
  countDistinct: Int!         # 去重计数
}
```

## 💡 使用示例

### 基本查询

获取用户列表：

```graphql
query {
  users(limit: 10, offset: 0) {
    items {
      id
      name
      email
    }
    total
  }
}
```

### 带条件的查询

使用复杂过滤器：

```graphql
query {
  posts(
    filter: {
      AND: [
        { published: { eq: true } },
        { 
          OR: [
            { title: { ilike: "%graphql%" } },
            { content: { ilike: "%api%" } }
          ]
        }
      ]
    },
    sort: [{ createdAt: DESC }],
    limit: 20
  ) {
    items {
      id
      title
      author {
        name
      }
    }
    total
  }
}
```

### 关联查询

带嵌套关联查询：

```graphql
query {
  users(
    filter: {
      posts: {
        viewCount: { gt: 100 }
      }
    }
  ) {
    items {
      id
      name
      posts(limit: 5, sort: [{ viewCount: DESC }]) {
        id
        title
        viewCount
      }
    }
  }
}
```

### 游标分页查询

使用游标进行分页：

```graphql
query {
  posts(first: 10) {
    items {
      id
      title
    }
    pageInfo {
      hasNext
      end         # 获取最后一项的游标，用于请求下一页
    }
  }
}

# 获取下一页
query {
  posts(first: 10, after: "eyJpZCI6MTB9") {
    items {
      id
      title
    }
  }
}
```

### 聚合查询

使用聚合函数：

```graphql
query {
  postsStats(
    filter: { 
      published: { eq: true } 
    },
    groupBy: {
      fields: ["authorId"],
      limit: 10,
      sort: { "count": "DESC" }
    }
  ) {
    count
    viewCount {
      sum
      avg
    }
    groupBy {
      key
      count
    }
  }
}
```

## 🚀 最佳实践

1. **查询优化**
   - 使用精确的过滤条件减少数据传输
   - 合理使用分页参数
   - 限制嵌套查询的深度
   - 使用包含必要字段的片段

2. **数据安全**
   - 实现字段级权限控制
   - 敏感过滤条件使用服务端验证
   - 防止过度复杂的查询导致性能问题

## 📘 开发指南

1. 使用TypeScript或GraphQL Code Generator生成类型定义
2. 查询时使用片段减少重复
3. 使用批量操作减少请求次数
4. 遵循命名约定保持一致性

## 📝 设计决策说明

### 分页结构简化

我们采用了扁平化的分页结构设计：
- 直接在`items`字段中返回实体对象数组，无需额外的嵌套层
- 关键的游标信息集中在`pageInfo`对象中
- 这种设计既保留了游标分页的全部功能，又简化了数据结构和客户端处理逻辑

相比传统的Relay Connection规范，我们的设计更加简洁直观，降低了学习成本和使用复杂度。

---

📄 *本文档由IdeaBase团队维护，最后更新于2023年10月* 