# 参考：配置

## Schema

| 键 | 类型 | 说明 |
| --- | --- | --- |
| `schema.file` | string | 锁定 GraphQL Schema artifact |
| `schema.default-limit` | int | 默认分页大小 |
| `schema.max-depth` | int | 最大选择集深度，0 表示不限制 |
| `schema.max-tokens` | int | 查询 token 上限 |
| `schema.max-cost` | uint | 查询成本上限 |
| `schema.introspection` | bool | 是否允许标准 introspection |
| `schema.persisted-only` | bool | 是否只允许已注册 operation |
| `schema.mapping` | map | 数据库类型到 GraphQL 类型映射 |

## Metadata

| 键 | 说明 |
| --- | --- |
| `metadata.file` | 文件 loader，支持 `{mode}` |
| `metadata.classes` | 类、字段、关系和治理覆盖 |
| `metadata.use-camel` | GraphQL 名称使用 camelCase |
| `metadata.use-singular` | 表名转 singular 类名 |
| `metadata.include-tables` | 白名单，支持尾部 `*` |
| `metadata.exclude-tables` | 黑名单，优先级更高 |
| `metadata.table-prefix` | 从表名去除的前缀 |
| `metadata.show-through` | 是否显示 through 关系 |

## Class

```yaml
metadata:
  classes:
    Post:
      table: cms_content
      operations: [select, insert, update, delete]
      id-generator: database
      scope:
        - column: tenant_id
          context: tenant
      soft-delete:
        column: deleted_at
      permissions:
        reader:
          select: [id, title]
```

Class 还支持 `primary_keys`、`fields`、`search`、`where`、`include_fields`、`exclude_fields`、`global` 和 `override`。

## Subscription 与搜索

```yaml
subscription:
  publication: ideabase_cdc
  dsn: ""
search:
  mode: tsvector   # tsvector / trigram / ilike
  config: jiebacfg
```

留空会自动探测；生产应显式记录实际模式，便于回滚和容量评估。

## 代码级选项

| API | 用途 |
| --- | --- |
| `WithLoader` | 添加必需 metadata loader |
| `WithOptionalLoader` | 添加可选 loader |
| `WithoutLoader` | 移除默认 loader |
| `WithIDGenerator` | 注册主键生成策略 |
| `WithCodecs` | 注册输入/输出 Codec |
| `Builder.Policy` | 设置唯一外部策略提供者 |
| `Builder.Security` | 设置安全状态提供者 |
| `Builder.SubscriptionLease` | 设置订阅授权刷新周期 |

---

[← Gateway 指南](../guides/gateway.md) · [文档首页](../README.md) · [下一章：GraphQL 能力参考 →](graphql.md)
