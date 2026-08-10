# GQL Gateway 迁移

应用主路径统一为 `New` → `Builder` → `Policy`/metadata permissions → `Build` → `Engine`；
低级构造器不再是运行时装配入口。
自定义业务统一注册 `Resolver`，Fiber 使用 `Bind`。单数据源使用 `Engine`；多租户数据源
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

Query、Mutation 和实体字段统一注册 `Resolver`。Mutation 业务代码必须从
`gql.DB(ctx)`/`gql.RequireDB(ctx)` 获取连接，并继续传给 service/repository；捕获全局
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
