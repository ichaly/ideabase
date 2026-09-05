# GQL Engine 文档

GQL Engine 是一个把数据库映射为 GraphQL 的 Go 引擎。它自动生成可自省的 Schema、把查询编译为参数化 SQL，并统一处理权限、租户、事务、审计、订阅和多实例组合。

这套文档按阅读目标组织，而不是按源码包组织：

## 教程：第一次运行

1. [安装与最小 Engine](tutorial/01-first-engine.md)
2. [第一次查询和 Mutation](tutorial/02-first-query.md)

## 指南：解决具体问题

1. [数据库映射与 Schema 配置](guides/database.md)
2. [多租户、权限和审计](guides/security.md)
3. [自定义 Resolver 与 Codec](guides/resolvers.md)
4. [Gateway、命名空间和多实例](guides/gateway.md)

## 参考：查 API 和协议

1. [配置参考](reference/configuration.md)
2. [GraphQL 能力参考](reference/graphql.md)

## 运维：上线和验证

1. [生产部署](operations/production.md)
2. [测试与性能](operations/testing.md)

## 先理解三个对象

| 对象 | 生命周期 | 责任 |
| --- | --- | --- |
| `Builder` | 启动期 | 注册 metadata、Resolver、Policy、文档和观测器 |
| `Engine` | 运行期 | 执行查询、Mutation、订阅并暴露快照/能力 |
| `Gateway` | 组合期/运行期 | 按命名空间或租户路由多个 Endpoint |

```text
数据库 catalog → Metadata → Schema/Capability → Compiler → SQL → Engine → JSON/SSE
```

## 示例代码

从 [`examples/orders`](../examples/orders/README.md) 开始 review 生产接入和业务闭环，再通过 [`examples/README.md`](../examples/README.md) 阅读博客的全能力矩阵，覆盖 CRUD、关系、分页、聚合、Resolver、远程实例组合、订阅和权限。

## 文档约定

- 每个章节先说明适用场景，再给最小代码，最后列出验证命令和失败行为。
- 文档中的 GraphQL 名称以运行时自省为准；不要依赖数据库字段猜测生成名称。
- 配置示例使用 YAML，Go 示例使用 `github.com/ichaly/ideabase/gql` 的公开 API。

---

[开始教程：安装与最小 Engine →](tutorial/01-first-engine.md)
