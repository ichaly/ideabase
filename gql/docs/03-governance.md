# 租户、权限与审计

## Identity 与 Scope

```go
identity, err := gql.NewIdentity(gql.IdentityInput{
    Tenant: "tenant-a", Subject: "user-1", Roles: []string{"reader"},
})
ctx = gql.WithIdentity(ctx, identity)
ctx = gql.WithScope(ctx, map[string]any{"tenant": "tenant-a"})
```

Identity 必须来自认证系统，不要信任客户端租户 Header。元数据声明 `posts.tenant_id <- context.tenant` 后，查询、关系、递归、create、upsert、update、delete 都自动注入租户条件；缺少 Scope 时不会退化为全表访问。

## 操作和字段权限

```yaml
metadata:
  classes:
    User:
      operations: [select, insert, update]
      permissions:
        reader:
          select: [id, name]
          insert: [name]
          update: [name]
          delete: false
        admin:
          select: ["*"]
          insert: ["*"]
          update: ["*"]
          delete: true
```

操作包含 `select`、`aggregate`、`insert`、`update`、`delete`、`restore`、`purge`。未命中角色、未声明操作或配置加载失败默认拒绝；静态拒绝应在 SQL 前完成，行谓词拒绝必须无未授权数据返回。

## 外部 PolicyProvider

```go
if err := builder.Policy(provider); err != nil { log.Fatal(err) }
```

PolicyProvider 是唯一外部策略入口，应返回字段、操作、行谓词、preset 和 check 的确定性决策。不要同时配置 metadata permissions。

## 审计与安全状态

审计列可配置为 `created_at`、`created_by`、`updated_at`、`updated_by`、`deleted_at`；值从身份和事务上下文生成，客户端不能覆盖。生产多实例必须共享 SecurityStateProvider、ReplayStore、审计/outbox；进程内实现仅用于开发。

## 生产安全开关

```yaml
schema:
  introspection: false
  persisted-only: true
```

同时设置最大深度、token 数、cost、请求体和响应体限制。启动时权限、迁移和安全状态错误必须 fail-fast，不能生成开放权限的 legacy Schema。

---

[← 上一章](02-data-and-operations.md) · [返回目录](README.md) · [下一章：Resolver 扩展 →](04-resolvers.md)
