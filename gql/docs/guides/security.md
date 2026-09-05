# 指南：多租户、权限与审计

## 注入 Identity

```go
identity, err := gql.NewIdentity(gql.IdentityInput{
    Tenant: "tenant-a", Subject: "user-1", Roles: []string{"reader"},
})
if err != nil { return err }
ctx = gql.WithIdentity(ctx, identity)
```

Identity 必须由认证中间件产生。不要把客户端 Header 直接当作租户身份。

## 配置行级 Scope

```yaml
metadata:
  classes:
    Post:
      scope:
        - column: tenant_id
          context: tenant
      global: false
```

```go
ctx = gql.WithScope(ctx, map[string]any{"tenant": "tenant-a"})
```

Scope 会进入查询、关系、递归和写入路径。缺失 Scope 时 fail-closed，不会退化为跨租户查询。

## 操作和字段权限

```yaml
metadata:
  classes:
    User:
      operations: [select, insert, update]
      permissions:
        reader:
          select: [id, name]
          delete: false
        admin:
          select: ["*"]
          insert: ["*"]
          update: ["*"]
          delete: false
```

此例 reader 只读，admin 可创建和更新；类级 operations 未开放 delete，因此即使角色允许也不会生成删除入口。

支持 `select`、`aggregate`、`insert`、`update`、`delete`、`restore`、`purge`。未命中角色、未声明操作、字段不在白名单或治理 loader 失败时拒绝。

## PolicyProvider

```go
if err := builder.Policy(provider); err != nil { return err }
```

PolicyProvider 是唯一外部策略入口。它可以返回字段集合、操作、行谓词、preset 和 post-write check；不能和 metadata permissions 同时配置。

## 审计和删除

配置 `created_at`、`created_by`、`updated_at`、`updated_by`、`deleted_at` 等审计列后，值从身份和事务上下文产生，客户端不能覆盖。purge 必须保留审计事实或写入 outbox。

## Delegation 和多实例

多实例需要共享 ReplayStore、SecurityStateProvider、路由控制状态和审计 outbox。delegation token 绑定租户、Subject、Roles、权限版本和路由版本；子节点验证后才执行。

---

[← 数据库映射](database.md) · [文档首页](../README.md) · [下一章：Resolver 扩展 →](resolvers.md)
