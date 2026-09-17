# GQL Gateway 迁移

应用主路径统一为 `New` → `Builder` → `Policy`/metadata permissions → `Build` → `Engine`；
低级构造器不再是运行时装配入口。
自定义业务统一注册 `Resolver`。HTTP 统一使用 `transport.NewHandler`；Fiber 等框架通过官方
`net/http` adaptor 挂载该 handler。旧 `Bind` 和 GraphQL WebSocket 入口已经移除，订阅统一
迁移到 `transport.NewHandler` 的 SSE。单数据源使用 `Engine`；多租户数据源
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

引擎入口直接使用 `*std.Connection`，已删除 `gql.Database`、`gql.DBTX` 和 `gql.NewDatabase` 薄封装。
原 `gql/database` 的连接封装已合并至 `std/connection.go`，不再提供独立子包。
推荐用 `std.OpenConnection(dsn)`，或 `std.NewConnection(config)`，创建一次公共连接对象；
GORM 使用其 `Pool()`，GQL 直接使用该对象，CDC 使用其 `DSN()`。
GORM 装配入口 `std.NewDatabase(entities, plugins, config, db)` 新增公共连接参数；
IoC 已同步注册 `std.NewConnection` 和 `std.NewDatabase` 两个构造器。
自定义驱动通过 `std.RegisterDatabase(name, std.Dialector{Open: ..., Gorm: ...}, aliases...)` 注册。
`Open` 返回底层池和实际 DSN，`Gorm` 接收 `*std.Connection` 创建共享池上的 GORM 方言。
具体 SQL 驱动名和连接串解析由驱动包内部处理。
应用应导入 `std/postgres` 或 `std/mysql` 注册完整驱动，只导入底层 SQL 驱动不再足够。
已移除 `WithDSN`、`WithDatabaseDSN` 和对应 Option 类型；DSN 只在 `OpenConnection(dsn)` 内保存。
已有 `*sql.DB` 仍可用 `std.WrapConnection(pool, driver)` 包装，但不保存 DSN，
使用 CDC 时需另配 `subscription.dsn`。Resolver 内部使用
`execution.RequireDB()` 返回的 `std.DBTX`，从而自动复用当前 `*sql.Tx`。

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

`transport.NewHandler` 要求显式传入一个
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
`WithExecutionContext`/`WithExecutionCapability`，也不再提供独立的 `Tx`
和包级 `Invalidate`。`DB()` 返回请求连接（可能为空），`RequireDB()` 在无连接时返回明确错误。协议内置表也不再以可变全局变量暴露：

- `protocol.DataTypes` → `protocol.DefaultDataTypes()`；
- `protocol.Operators` → `protocol.GetOperator(name)` / `protocol.OperatorsForScalar(type)`；
- `protocol.Grouping` → `protocol.ScalarTypes()` 与对应查询函数。

这些是 GA major 边界的显式编译期不兼容项，目的是消除跨 Engine 全局污染和同义 API。

## Resolver 与 HTTP 概念收敛

- `NewRemote` 改为 `NewLoader`，返回同一个 `Resolver`；删除 `Remote`、`RemoteRef` 和单独的 Remote 注册/执行路径。
- `Builder.Register` 只接受 `Resolver`。普通、批量、按键加载均经此入口；策略只通过 `Builder.Policy` 配置。
- `Handler` 声明接口并入 `Resolver`，不再额外实现。直接实现 Resolver 的实体字段也从 `Define` 挂载。
- `Replace` 自身声明覆盖意图，删除 `Existing()` 和 `Define.Existing`；`Register` 仍拒绝覆盖已有字段。
- 批量中间件的 source/result 类型使用切片，例如 `NextResolver[[]Source, Args, []Value]`，保持一次批量调用。
- `NewSubscriptionHandler` 合并到 `NewHandler`。只挂载 `/graphql`；SSE 请求使用 POST JSON 和 `Accept: text/event-stream`。长连接与普通请求分别限流。
- capability 的 `handler: remote` 统一为 `handler: resolver`。发布时重新获取 manifest/effective schema 和 snapshot pin；旧的结构指纹不能继续使用。
- 示例只暴露带租户路由的 `/graphql`。Gateway 同样可挂载该路径，namespace 出现在 GraphQL Schema 中，而非要求另一个 URL。

远程实例组合仍使用 `RemoteEndpoint`：它提供整个服务的 Schema 和执行契约，不是字段加载器。SQL 快路径、三种租户隔离、多副本共享撤权/重放与订阅能力保留。

## 全量生产代码精简

- 运行时权限的 `gql.TypedPredicate` 改为 `gql.Predicate`，与 `protocol.Predicate` 是同一个类型；不再复制结构或逐项转换。JSON 字段统一为 `column`、`operator`、`context`、`value`。
- 删除独立的 `gql/renderer` 子包。Schema 生成直接使用 SDL 类型字符串和已有的 `gql.Argument`；应用继续通过 `Builder.Build` / `Engine.Schema` 获取 Schema。
- SDL 字段参数统一行内输出。语义不变，但原始 Schema 文本与结构指纹会变化，需重新获取 Schema、manifest 和执行 pin。
- 删除仅在包内使用的 CDC、搜索探测注册表；公开 SQL 方言注册入口保持不变。

完整审阅范围与验证结果见 [生产代码精简记录](docs/reviews/2026-09-05-production-simplification.md)。

## 五轮精简后的单一入口

- `transport.Endpoint` 别名删除，直接使用 `gql.Endpoint`；健康、订阅和有效 Schema 的传输实现复用核心包接口。
- 删除 `gql.HookedLoader`。自定义元数据加载器直接实现 `protocol.Loader`；内置数据库加载器的开发环境缓存由 `NewMetadata` 统一保存。
- `Class.PrimaryConflictTarget()` 改为 `Class.ConflictTarget("PRIMARY")`；`Class.PrimaryUpsertConflictTarget()` 改为 `Class.UpsertConflictTarget("PRIMARY")`。
- 内部配置复用 `protocol.ScopeRule`、`ValueSource`、`SoftDelete`、`Predicate`、`Permission`，配置键保持不变。

以上 Go 符号删除属于编译期不兼容调整；GraphQL 请求、响应、租户隔离和权限语义保持不变。
验证与性能对比见 [五轮精简记录](docs/reviews/2026-09-05-five-round-simplification.md)。

## 生产 catalog 来源

连接数据库后，dev/prod 均默认读取数据库 catalog。需要固定 artifact 的部署必须显式设置
`metadata.file`；该文件替代内置数据库加载器，加载失败仍拒绝启动。没有数据库的文件/配置模式保留。
旧部署若依赖隐式 `cfg/metadata.prod.json`，升级前应显式配置路径，或允许启动账号读取 catalog。
