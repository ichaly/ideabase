# 教程：第一次查询和 Mutation

## 查询列表

生成的实体通常提供 `items`、`total` 和分页参数：

```graphql
query Users($limit: Int!, $offset: Int!) {
  users(limit: $limit, offset: $offset) {
    items { id name email }
    total
  }
}
```

HTTP 请求：

```json
{
  "operationName": "Users",
  "variables": {"limit": 20, "offset": 0}
}
```

## 过滤和排序

```graphql
query {
  posts(
    where: { title: { contains: "Go" }, public: { eq: true } }
    sort: [{ createdAt: DESC_NULLS_LAST }, { id: ASC }]
    limit: 20
  ) {
    items { id title }
    total
  }
}
```

支持组合条件、比较/字符串/数组/NULL/JSON 操作符、多字段排序和 `distinct`。具体可用字段以自省结果为准。

## 游标分页

```graphql
query Page($first: Int, $after: Cursor) {
  posts(first: $first, after: $after, sort: { id: ASC }) {
    items { id title }
    pageInfo { hasNext hasPrev start end }
  }
}
```

下一页把 `pageInfo.end` 原样作为 `after`；反向分页使用 `last` 和 `before`。游标绑定租户、策略、排序和 Schema 版本，不可跨上下文复用。

## 创建、更新和删除

```graphql
mutation Create($input: UserCreateInput!) {
  createUser(input: $input) { affected items { id name } }
}
```

```json
{"operationName":"Create","variables":{"input":{"name":"Alice"}}}
```

通用操作包括 `create`、`update`、`delete`、`upsert`、`restore` 和 `purge`。soft delete 会写删除时间列并从普通查询隐藏；purge 是物理删除，只在显式授权和审计能力可用时生成。

## 关系和聚合

实体关系可在同一选择集中嵌套；PostgreSQL 路径编译为单条 SQL，避免逐行 N+1。生成的聚合入口支持 `count`、列统计、`groupBy` 和 `having`。完整样例见 [`examples/queries`](../../examples/queries)。

## 事务语义

同一个 GraphQL Mutation 的顶层字段按顺序执行并共享事务边界；Resolver 可通过 `gql.Tx(ctx)` 使用当前事务。错误会按 GraphQL 错误规范返回，并保留稳定 `extensions.code`。

---

[← 上一章](01-first-engine.md) · [文档首页](../README.md) · [下一步：数据库映射 →](../guides/database.md)
