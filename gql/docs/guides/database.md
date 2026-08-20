# 指南：数据库映射与 Schema

## 什么时候用配置

数据库 catalog 是事实来源；配置只负责命名、范围、治理和少量声明。不能用配置制造编译器没有实现的 GraphQL 能力。

```yaml
schema:
  file: ./cfg/schema.graphql
  default-limit: 50
  max-depth: 12
  max-tokens: 10000
  max-cost: 100000
metadata:
  use-camel: true
  use-singular: true
  include-tables: [user_*, post]
  exclude-tables: [audit_*]
  table-prefix: [cms_]
```

`schema.file` 用于锁定 Schema artifact；启动时类型、参数、输入字段和坐标漂移会被拒绝。`metadata.file` 用于补充表、字段、关系、Scope、权限和删除治理。

## 表、字段和关系

表映射为对象类型，主键生成 `ByPk` 和定位输入，外键生成关系字段。支持一对一、多对一、一对多、多对多和自关联。关系查询、递归查询和 batch Resolver 应优先保持单 SQL。

字段可配置：

- include/exclude
- description
- GraphQL 名称和类型映射
- search 字段
- ID generator
- nullable/default/generated 属性

## ID 生成

- `database`：自增、UUID default 或数据库生成列
- `snowflake`：内置 Snowflake
- 自定义名称：`WithIDGenerator(name, generator)`

数据库生成列且没有可写字段时，只有在引擎和方言能证明 `DEFAULT VALUES` 可执行时才生成 create。

## 搜索能力

```yaml
search:
  mode: tsvector   # tsvector / trigram / ilike
  config: jiebacfg
```

为空时自动探测；`ilike` 是降级路径。搜索能力是否出现于 Schema 取决于数据库能力和配置校验。

## 自省和 Capability

标准 GraphQL introspection 展示类型和字段；Engine 的 Snapshot/Manifest/Effective Schema 额外展示资源、操作、成本、重试安全、幂等键、最大 batch 等执行元数据。生产 Agent 应缓存 identity-aware artifact，而不是猜测私有字段。

---

[← 教程](../tutorial/02-first-query.md) · [文档首页](../README.md) · [下一章：安全治理 →](security.md)
