# Resolver 扩展

通用 CRUD 由数据库映射提供；Resolver 只承载业务计算、外部服务和不可声明的流程。Resolver 与 Action 使用同一个“字段扩展”心智模型。

## 实体字段

```go
resolver := gql.NewResolver[User, map[string]any, string](
    "User", "greeting", "Personal greeting",
    func(ctx context.Context, source gql.Source, input map[string]any) (string, error) {
        return "Hello, " + fmt.Sprint(source["name"]), nil
    },
)
builder.Register(resolver)
```

## Batch Resolver

```go
resolver := gql.NewBatch[map[string]any, struct{}, string](
    "User", "reputation", "Reputation",
    func(ctx context.Context, sources []gql.Source, input struct{}) ([]string, error) {
        return loadReputations(ctx, sources) // 一次批量加载，顺序与 sources 对齐
    },
)
builder.Register(resolver)
```

Batch 是避免 N+1 的标准路径；不要在普通 Field Resolver 中逐行查询数据库。

## Root、Replace、Wrap

`NewResolver("Query", ...)` 和 `NewResolver("Mutation", ...)` 用于根字段；`Replace` 覆盖生成字段；`Wrap("User.greeting", middleware)` 添加日志、指标或统一错误处理。所有 Resolver 都经过身份、策略、事务和稳定错误码处理。

## Remote Join

```go
remote := gql.NewRemote("User", "reputation", "Reputation", "id",
    func(ctx context.Context, keys []any) (map[any]Reputation, error) {
        return reputationService.BatchGet(ctx, keys)
    })
builder.Register(remote)
```

Remote 按宿主键批量回填，不声明数据库生命周期，也不绕过子 Engine 的权限边界。

## Codec 与访问声明

Codec 用于 ID 编码、自定义标量、输入/输出边界转换；`WithCodecs` 注册后只在选择相关字段时执行。Resolver 可附带访问坐标和文档，确保 Capability 与自省可发现。

---

[← 上一章](03-governance.md) · [返回目录](README.md) · [下一章：Gateway 与传输 →](05-gateway-and-transport.md)
