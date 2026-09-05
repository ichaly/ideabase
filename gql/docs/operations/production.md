# 运维：生产部署

## 最小生产配置

```yaml
schema:
  default-limit: 50
  max-depth: 12
  max-tokens: 10000
  max-cost: 100000
subscription:
  publication: ideabase_cdc
```

完整可运行装配见 [订单示例](../../examples/orders/README.md)。若要限制为预登记操作，再启用 persisted-only 并加载相应文档。

生产 PostgreSQL Engine 必须显式安装 SecurityStateProvider；Build 时会检查 metadata、权限、搜索能力和必需迁移。

## 启动和 readiness

readiness 应检查：数据库 Ping、schema/capability fingerprint、control/policy/route revision、CDC publication/slot、replay/security/audit/outbox 表、索引和权限。缺少治理迁移不能返回 ready。

## 多实例

副本可以独立运行 Engine，但必须共享 replay、撤权、安全状态、路由控制和审计 outbox。使用 CAS 或单调 revision 发布配置；请求通过 execution pin 固定快照，避免滚动发布期间混用 Schema。

## 迁移原则

先迁移控制面表和索引，再发布依赖它们的 Engine；回滚时保留旧版本所需字段和 artifact。purge 必须和审计事实处在同一事务或明确的 outbox 交付契约中。

## 安全边界

限制深度、cost、body 和 response；远程组合通过 delegation verifier 绑定身份、租户、策略和路由版本。不要用普通 Header 传递可信租户。面向固定客户端时可关闭 introspection 并启用 persisted-only；通用管理后台可按实际需要保留受权限约束的动态查询。

---

[← GraphQL 参考](../reference/graphql.md) · [文档首页](../README.md) · [下一章：测试与性能 →](testing.md)
