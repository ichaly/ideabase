# GQL Engine 使用文档

GQL Engine 将数据库元数据、GraphQL Schema、参数化 SQL、权限和运行时治理组合成一个网关。数据库负责声明通用数据能力，业务代码只实现不可通用的 Resolver。

## 导航

| 章节 | 内容 |
| --- | --- |
| [快速开始](01-quick-start.md) | 安装、组装 Engine、HTTP 接入 |
| [数据库映射与 CRUD](02-data-and-operations.md) | 表、关系、过滤、分页、聚合、Mutation |
| [租户、权限与审计](03-governance.md) | Identity、Scope、Policy、软删、安全 |
| [Resolver 扩展](04-resolvers.md) | Field、Batch、Root、Remote、Codec、Middleware |
| [Gateway 与传输](05-gateway-and-transport.md) | Namespace、TenantGateway、HTTP、SSE、控制面 |
| [生产与测试](06-production.md) | 多实例、迁移、性能和发布检查 |

## 核心路径

```text
数据库表/关系 → metadata → 冻结 Schema + Capability → compiler → 参数化 SQL → Engine → JSON/SSE
```

原则：Builder 只组装一次；Engine 构建后不可变；权限默认拒绝；Schema 自省必须和真实执行能力一致；查询优先单 SQL、计划缓存和字节响应快路径。

完整可运行样例见 [`examples/README.md`](../examples/README.md)，覆盖 CRUD、关系、分页、Resolver、Gateway、订阅、权限和性能门禁。

---

[下一章：快速开始 →](01-quick-start.md)
