# 参考：GraphQL 能力

## 生成的根能力

对每个满足元数据和权限条件的实体，Schema 可能包含：

| 类别 | 能力 |
| --- | --- |
| 查询 | list、by primary key、relations、recursive |
| 过滤 | and/or/not、比较、字符串、数组、NULL、JSON |
| 排序 | 多列、ASC/DESC、NULLS FIRST/LAST |
| 分页 | limit/offset/total、first/after、last/before |
| 聚合 | count、统计、groupBy、having、distinct |
| 写入 | create、batch create、update、delete、upsert |
| 生命周期 | restore、purge、soft delete |
| 关系写入 | nested create、connect、disconnect（按关系约束生成） |

没有数据库、策略或方言执行证明的能力不会出现在 Schema 中。

## 生成输入

典型类型包括 `WhereInput`、`SortInput`、`CreateInput`、`UpdateInput`、`Connection`、`Edge`、`PageInfo`、`MutationResult` 和 `DeletionMode`。字段是否 nullable、可写或可过滤由 catalog、数据库 default 和 policy 决定。

## 自省与能力 artifact

标准 GraphQL introspection 负责类型和字段；Snapshot/Manifest/Effective Schema 负责 capability 元数据：resource、actions、cost、retry-safe、idempotency key、max batch、revision 和 fingerprint。Agent 或 Gateway 应把 artifact 当作单一能力发现入口。

## 查询限制

`max-depth`、`max-tokens`、`max-cost`、分页上限和 HTTP body/response 上限在解析或执行前生效。persisted-only 模式下，原始 query 被拒绝，只接受 Build 时已校验的 operationName。

## 错误形状

GraphQL 执行错误：

```json
{"errors":[{"message":"...","extensions":{"code":"FORBIDDEN"}}]}
```

HTTP 请求错误：

```json
{"error":{"code":"INVALID_REQUEST","category":"request","retryable":false,"message":"..."}}
```

客户端应按 `code` 和 `retryable` 决策修正、重新认证、退避或终止，不应解析 message。

---

[← 配置参考](configuration.md) · [文档首页](../README.md) · [下一章：生产部署 →](../operations/production.md)
