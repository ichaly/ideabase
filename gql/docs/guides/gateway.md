# 指南：Gateway、命名空间和多实例

## 命名空间组合

```go
gateway, err := gql.NewGateway("main",
    gql.Namespace{Name: "blog", EndpointID: "blog-1", Endpoint: blogEngine},
    gql.Namespace{Name: "shop", EndpointID: "shop-1", Endpoint: shopEngine},
)
```

Gateway 合并子 Endpoint 的可执行 Schema、Capability 和控制版本；执行、订阅和权限仍固定在子节点快照。

## 租户路由

```go
router, err := gql.NewTenantRouter(gql.TenantRouterConfig{
    Mode: gql.TenantModeDatabase, Revision: 1,
    Routes: map[string]gql.TenantRoute{
        "tenant-a": {DataSource: "primary"},
    },
})
gateway, err := gql.NewTenantGateway(router, sources,
    gql.WithSecurityState(securityProvider))
```

路由 revision 参与快照、游标和订阅身份；刷新必须单调或使用 CAS，防止迟到配置回滚。

## HTTP 子节点合同

远程 Endpoint 应提供：

| 端点 | 用途 |
| --- | --- |
| `/manifest` | 静态 Schema、Capability、控制 revision |
| `/effective-schema` | 按 delegation identity 过滤的 Schema artifact |
| `/graphql` | 查询、突变与固定快照的 SSE |
| `/health` | DB、CDC、策略、迁移和版本状态 |
| `/.well-known/gql-gateway` | 传输发现描述 |

Gateway 不能只使用静态 Manifest 生成身份相关 Schema；需要 Effective Schema 并校验 fingerprint/revision。

## 多副本共享状态

以下状态不能使用进程内实现：ReplayStore（token replay）、SecurityStateProvider（撤权）、路由控制状态和审计 outbox。子节点验证签名 delegation 后，才允许执行或订阅。

---

[← Resolver 扩展](resolvers.md) · [文档首页](../README.md) · [下一章：配置参考 →](../reference/configuration.md)

独立 Engine、TenantGateway 和组合后的 Gateway 使用同一个传输入口：

```go
mux.Handle("/graphql", transport.NewHandler(gateway, transport.Options{}))
```

不需要额外的 `/gateway/graphql` 或 `/tenant/graphql`。选择服务装配方式后，客户端只使用一个业务 URL。
