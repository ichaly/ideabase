# 数据库映射与 CRUD

## 数据库到 Schema

Engine 读取表、列、主键、索引和外键，生成对象、输入、连接、分页和 Mutation 类型：`users` 通常映射为 `User`，外键映射为关系字段。`use-camel`、`use-singular`、`include-tables`、`exclude-tables` 和 `table-prefix` 可调整命名及范围。

```yaml
schema:
  default-limit: 50
  max-depth: 12
  max-tokens: 10000
  max-cost: 100000
metadata:
  use-camel: true
  use-singular: true
  include-tables: [user_*, post]
  exclude-tables: [audit_*]
```

配置不能声明引擎没有实现的能力；最终 Schema、Capability 和 Effective Schema 是客户端的事实来源。

## 查询

```graphql
query ($limit: Int!, $offset: Int!) {
  posts(
    where: { title: { contains: "数据库" }, and: [{ public: { eq: true } }] }
    sort: [{ createdAt: desc }]
    limit: $limit
    offset: $offset
  ) {
    items { id title author { id name } }
    total
  }
}
```

支持 `and/or/not`、比较/字符串/数组/NULL/JSON 操作符、多字段排序、`distinct`、`count`、列聚合、`groupBy`、`having`、alias、fragment、variables、`__typename`、关系和递归查询。PostgreSQL 嵌套查询优先编译为单条 SQL，避免 N+1。

## 分页

Offset 使用 `limit/offset/total`；游标使用 `first/after` 或 `last/before`：

```graphql
query ($first: Int, $after: String) {
  posts(first: $first, after: $after, sort: [{ createdAt: desc }]) {
    items { id title }
    pageInfo { hasNextPage hasPreviousPage startCursor endCursor }
  }
}
```

游标绑定 Schema、策略、排序、租户和身份版本，不能跨上下文复用或篡改。

## Mutation 与删除

生成 `create/update/delete/upsert/restore/purge`，是否出现由实体操作和权限决定：

```graphql
mutation {
  createUser(input: { name: "Alice" }) { affected items { id name } }
  updateUser(where: { id: { eq: "1" } }, input: { name: "Bob" }) { affected }
  deleteUser(where: { id: { eq: "1" } }) { affected }
}
```

Mutation 在事务中按顶层字段顺序执行。soft delete 自动写删除列并隐藏已删除行；restore 恢复；purge 物理删除且必须显式授权并保留审计事实。关系写入支持 nested create、connect、disconnect 及受支持的批量操作。

## 搜索、ID 与自省

搜索支持 `tsvector`、`trigram` 和 `ilike` 降级模式；ID 支持数据库生成、Snowflake 和 `WithIDGenerator` 注册策略。标准 GraphQL introspection、`Snapshot`、Manifest 和 Effective Schema 展示真实可执行能力。

---

[← 上一章](01-quick-start.md) · [返回目录](README.md) · [下一章：租户、权限与审计 →](03-governance.md)
