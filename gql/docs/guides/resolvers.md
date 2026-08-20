# 指南：Resolver、Remote 和 Codec

通用 CRUD 不需要手写 Resolver。只有业务计算、外部服务或真正不能声明的工作流才使用扩展字段。

## Field Resolver

```go
resolver := gql.NewResolver[User, map[string]any, string](
    "User", "greeting", "Personal greeting",
    func(ctx context.Context, source gql.Source, input map[string]any) (string, error) {
        return "Hello, " + fmt.Sprint(source["name"]), nil
    },
    gql.Access("users", "read"),
)
builder.Register(resolver)
```

## Batch Resolver

```go
resolver := gql.NewBatch[map[string]any, struct{}, string](
    "User", "reputation", "Reputation",
    func(ctx context.Context, sources []gql.Source, input struct{}) ([]string, error) {
        return loadReputations(ctx, sources)
    },
)
builder.Register(resolver)
```

返回顺序必须和 `sources` 对齐。Batch 是避免 N+1 的唯一推荐路径。

## Root Resolver

`NewResolver("Query", ...)` 和 `NewResolver("Mutation", ...)` 用于复杂根字段。它们仍然经过统一的身份、策略、事务、错误和 capability 流程，不应绕过 Engine 直接写数据库。

## Remote Join

```go
remote := gql.NewRemote("User", "reputation", "Reputation", "id",
    func(ctx context.Context, keys []any) (map[any]Reputation, error) {
        return service.BatchGet(ctx, keys)
    })
builder.Register(remote)
```

Remote 按宿主键批量回填，不声明数据库 CRUD，也不能绕过子 Engine 的租户和权限边界。

## Replace、Wrap 和语义

```go
builder.Replace(customResolver)
builder.Wrap("User.greeting", middleware)
```

`Replace` 覆盖生成字段，`Wrap` 添加日志、指标或错误处理。使用 `Semantic(Primitive)` 或 `Semantic(Workflow)` 让 capability 消费者知道字段语义；复杂 Workflow 不应伪装成数据库原语。

## Codec

`WithCodecs` 注册 ID、日期或自定义标量的输入/输出转换。未选择 Codec 字段时不会触发解包，因此不会破坏普通查询字节快路径。

---

[← 安全治理](security.md) · [文档首页](../README.md) · [下一章：Gateway →](gateway.md)
