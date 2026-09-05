# 教程：创建第一个 Engine

需要 Go 1.26.6、PostgreSQL 15–18。推荐从 [可运行的订单示例](../../examples/orders/README.md)
开始：它使用真实生产模式、受限数据库账号和签名身份，不需要先生成元数据文件。

## 最少需要理解的内容

1. **数据库**：表、约束、索引决定自动生成的数据 API。
2. **权限与身份**：认证中间件验证登录声明，策略决定允许的操作；租户隔离由 scope 与 RLS 强制执行。
3. **Resolver**：只实现数据库 CRUD 无法表达的业务命令，如下单和取消。

应用通过 `gql.New(config, db)` 创建 Builder，在 `Build()` 前安装唯一权限提供者和生产
安全状态提供者，注册业务 Resolver。`Build()` 冻结 Schema 和执行配置；缺失权限、安全
状态或必需迁移时明确失败。

完整装配以 [app.go](../../examples/orders/app.go) 为唯一可执行示例，避免复制一份会
逐渐过期的启动程序。数据库与应用共享 `*sql.DB`，不需要第二个 ORM 或连接池。

## 查询、突变与订阅

同一个 `transport.NewHandler(endpoint, options)` 挂载 `/graphql`：普通请求使用 JSON，
订阅使用 POST JSON 与 `Accept: text/event-stream`。JWT 认证示例见
[http.go](../../examples/orders/http.go)，不会信任客户端提供的租户 Header。

验证命令 `go test ./examples/orders` 自动启动隔离的测试数据库，覆盖第一次启动、查询、
下单扣库存、取消回补、租户隔离、权限和实例组合。

## 接入自己的应用

复制示例的数据库/Builder 装配方式，复用应用已有的认证、迁移和观测工具。将 `orderPolicy`
替换为自己的角色规则，`orders.go` 替换为业务命令；不必复制示例的本地 token 签发或容器凭据。

认证后的客户端可以使用标准自省。若业务只允许预先登记的查询，可再启用 persisted-only，
并通过 `LoadDocuments` 登记操作文档；该限制不是普通管理后台启动的隐式前提。
