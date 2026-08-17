# GQL Gateway 迁移

应用主路径统一为 `New` → `Builder` → `Policy`/metadata permissions → `Build` → `Engine`；
低级构造器不再是运行时装配入口。
自定义业务统一注册 `Resolver`。HTTP 统一使用 `transport.NewHandler`；Fiber 等框架通过官方
`net/http` adaptor 挂载该 handler。旧 `Bind` 和 GraphQL WebSocket 入口已经移除，订阅统一
迁移到 `transport.NewSubscriptionHandler` 的 SSE。单数据源使用 `Engine`；多租户数据源
路由使用 `TenantGateway`，跨引擎命名空间组合使用 `Gateway`，三者都经由同一执行契约暴露。

## 1. 先升级引擎，再迁调用方

先发布包含本文件的 `github.com/ichaly/ideabase/gql` 版本并更新调用方 `go.mod`，然后在
`GOWORK=off` 下构建。仅靠本地 `go.work` 会遮蔽旧 pseudo-version；使用新 API 的应用在
独立 CI 中会出现 `undefined`。

```sh
GOWORK=off go test ./internal/modules/gql/api ./internal/modules/bot/api
```

## 2. 收紧生产边界

`Builder` 默认 fail-closed。为每个生产实体声明 `operations` 与 `permissions`，由认证层
构造 `gql.Identity`/scope；不要接受客户端传入的 tenant、role 或 actor。本地工具也要配置
私有 PolicyProvider；生产代码不得绕过 fail-closed policy。

推荐顺序：先部署完整权限配置与身份注入，观察拒绝指标，再逐步切换实例。

## 3. Resolver 复用请求事务

Query、Mutation 和实体字段统一注册 `Resolver`。Mutation 业务代码必须通过
`gql.ExecutionContextFromContext(ctx)` 取得唯一执行视图，再调用 `execution.RequireDB()` 获取
连接并继续传给 service/repository；捕获全局
`*gorm.DB` 或在 service 内另开事务不会加入当前 GraphQL Mutation 的原子边界。

跨 namespace Mutation 没有分布式事务：字段按文档顺序执行，响应的 commit metadata
报告已提交或状态未知的 namespace，引擎不会重放可能已经提交的操作。

## 4. 滚动数据库变更

使用 expand/contract：

1. additive DDL，保证旧/新 snapshot 都可运行；
2. 构建并验证新 snapshot，按 tenant cohort 原子切换；
3. 排空旧请求、订阅和 prepared plan；
4. 最后执行 destructive cleanup。

catalog fingerprint、policy revision 或 topology fingerprint 改变时不得复用旧 plan、cursor
或远程 snapshot。撤权和 tenant disable 走 revocation barrier，不使用 LKG；只有结构性
Schema/拓扑刷新失败可继续服务已验证的旧 snapshot。

## 5. 订阅语义

内置 CDC 订阅是 latest-state 查询流：重连会重新读取当前结果，不回放断线期间的每个
事件。需要 durable replay 时使用 outbox/事件表建立显式业务契约，不要把临时 WAL slot
当作事件存储。

## 6. HTTP transport 只接受一个配置对象

`transport.NewHandler` 与 `transport.NewSubscriptionHandler` 统一要求显式传入一个
`transport.Options`。无自定义配置时传 `transport.Options{}`；不要再传多个 options，避免
安全配置被静默覆盖。

## 7. 先执行生产迁移

集群实例启动前必须由应用迁移系统执行三份显式 DDL：

- `gql.PostgresReplayDDL`：跨副本 delegation token 单次消费；
- `gql.PostgresSecurityStateDDL`：跨副本撤权、credential、policy 与 route revision；
- `pgsql.AuditOutboxDDL`：物理删除审计事实。

共享 store 构造器和生产 purge Engine 会在启动期校验这些迁移，缺失时 fail closed，不再等到首个请求暴露。

## 8. GA transport 与 Resolver 元数据

- 为第三方客户端挂载 `transport.NewTransportDescriptorHandler`；客户端用
  `transport.DiscoverRemoteEndpoint` 发现后仍进入唯一 `RemoteEndpoint` 安全校验路径。
- 自定义 Handler 用 `gql.Semantic(gql.SemanticPrimitive|SemanticProjection|SemanticCommand|SemanticWorkflow)`
  声明工具语义；未声明时按根类型和 Handler 类型确定性推导。
- 嵌套关系写 capability 的 `refreshRequired=true` 表示写后应重新查询关系；不要假设同一
  PostgreSQL mutation CTE 的读回能看到关系子操作。
- 一致读场景显式使用 `gql.WithReadSnapshot(ctx)`；默认 Query 不额外开启事务。
- 自定义 Mutation 需要唤醒 latest-state 订阅时调用 `execution.Invalidate`，发布严格发生在引擎事务提交后。

## 9. GA major API 收敛

GA 以单一路径替换预发布兼容入口：Resolver 只使用 `ExecutionContext`，不再暴露可伪造的
`WithExecutionContext`/`WithExecutionCapability`，也不再并列提供 `DB`、`RequireDB`、`Tx`
和包级 `Invalidate`。协议内置表也不再以可变全局变量暴露：

- `protocol.DataTypes` → `protocol.DefaultDataTypes()`；
- `protocol.Operators` → `protocol.GetOperator(name)` / `protocol.OperatorsForScalar(type)`；
- `protocol.Grouping` → `protocol.ScalarTypes()` 与对应查询函数。

这些是 GA major 边界的显式编译期不兼容项，目的是消除跨 Engine 全局污染和同义 API。
