# Gateway 与传输

## Namespace Gateway

```go
gateway, err := gql.NewGateway("main",
    gql.Namespace{Name: "blog", EndpointID: "blog-1", Endpoint: blogEngine},
    gql.Namespace{Name: "shop", EndpointID: "shop-1", Endpoint: shopEngine},
)
```

Gateway 合并命名空间的可执行 Schema、Capability 和控制版本；执行仍保留子 Engine 的租户、权限和快照边界。身份相关 Schema 必须从 Effective Schema 获取，不能只使用静态 Manifest。

## TenantGateway

```go
router, _ := gql.NewTenantRouter(gql.TenantRouterConfig{
    Mode: gql.TenantModeDatabase, Revision: 1,
    Routes: map[string]gql.TenantRoute{"tenant-a": {DataSource: "primary"}},
})
gateway, _ := gql.NewTenantGateway(router, sources,
    gql.WithSecurityState(securityProvider))
```

路由 revision 参与快照、游标和订阅身份；刷新使用单调 revision/CAS。

## HTTP、SSE 和控制面

```go
transport.NewHandler(engine, transport.Options{})
transport.NewSubscriptionHandler(engine, transport.Options{})
transport.NewHealthHandler(engine)
transport.NewManifestHandler("node-1", engine, authenticate)
transport.NewEffectiveSchemaHandler(engine, verifier)
```

建议暴露 `/graphql`、`/graphql/stream`、`/health`、`/manifest`、`/effective-schema` 和 `/.well-known/gql-gateway`。HTTP 提供 body/response/concurrency/queue/timeout 限制、Accept 协商、persisted operation 和稳定错误码。

SSE 首次推送当前结果，CDC 唤醒后仅在最终结果变化时推送；同构订阅共享一次重查，慢消费者只保留最新状态。

## 多实例

多副本必须共享 ReplayStore、SecurityStateProvider、路由控制状态和审计 outbox。delegation token 绑定租户、Subject、Roles、权限版本和路由版本；子节点验证后才执行。

---

[← 上一章](04-resolvers.md) · [返回目录](README.md) · [下一章：生产与测试 →](06-production.md)
