# 多租户订单管理 API

这是 GQL 的生产模式接入示例：数据库生成客户、商品、订单及明细查询；业务代码仅实现
`placeOrder` 和 `cancelOrder` 两个 Resolver。事务、权限、租户路由、SQL 编译和订阅由引擎提供。
完整源码分别位于 `schema.sql`（数据约束）、`orders.go`（业务）、`app.go`（装配）和 `http.go`（JWT）。

## 运行

需要 Go 1.26.8、Docker。以下凭据仅用于本机一次性数据库。

```bash
cd gql/examples/orders
docker compose up -d --wait
go build -o /tmp/ideabase-orders .
export ORDER_TENANTS=a,b
export JWT_SECRET=$(openssl rand -hex 32)
DATABASE_URL='postgres://postgres:local-migration-only@localhost:5434/postgres?sslmode=disable' /tmp/ideabase-orders -migrate
# 初始化本地业务数据；实际项目使用自己的数据管理/迁移流程。
docker compose exec -T postgres psql -U postgres -v ON_ERROR_STOP=1 <<'SQL'
INSERT INTO customers(tenant_id,name) VALUES ('a','Alice'),('b','Bob');
INSERT INTO products(tenant_id,name,price_cents,stock) VALUES ('a','Tea',100,10),('b','Coffee',200,10);
SQL
export DATABASE_URL='postgres://orders_app:local-orders-only@localhost:5434/postgres?sslmode=disable'
export CDC_DATABASE_URL='postgres://orders_cdc:local-cdc-only@localhost:5434/postgres?sslmode=disable'
export TOKEN=$(/tmp/ideabase-orders -token -tenant a -role operator)
/tmp/ideabase-orders &
ORDERS_PID=$!
```

`-migrate` 只初始化空数据库，不是迁移框架；升级使用应用已有迁移工具。
在同一终端调用（结束后执行 `kill "$ORDERS_PID"`；部署时运行编译后的二进制并由进程管理器管理）：

```bash
curl http://127.0.0.1:8080/graphql -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"query":"mutation { placeOrder(requestId:\"order-001\",customerId:1,items:[{productId:1,quantity:2}]) { id status } }"}'
curl http://127.0.0.1:8080/graphql -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"query":"{ orders(limit:20) { items { id status customer { name } orderItems { quantity priceCents product { name } } } total } }"}'
# 将返回的 UUID 传入 cancelOrder(id:"...")；重复取消不会重复回补库存。
curl -N http://127.0.0.1:8080/graphql -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream' \
  -d '{"query":"subscription { products { items { id name stock } } }"}'
```

实际登录系统签发 HS256 JWT：`iss=orders-auth`、`aud=orders-api`、`sub`、`tenant`、
`roles=[operator|viewer]`、`exp`，可附带用于撤权的 `epoch`。`-token` 仅供本地验收；没有
公开的任意身份签发接口。只读角色可查询与订阅，操作员另外可维护客户、下单、取消。JWT 到期时活动 SSE 自动结束。
租户值仅来自签名声明，任何 `X-Demo-Tenant` 一类客户端 Header 都不能覆盖它。

## 业务契约

- `requestId` 在租户内唯一；相同规范化购物篮重试返回同一订单，不重复扣库存；改变购物篮则拒绝。
- 购物篮最多 100 种商品，每种数量 1–10000；重复商品拒绝，金额使用整数分。
- 商品按主键顺序锁定，整单库存不足、外租户客户/商品或任一步失败均回滚。
- 取消锁定订单后回补库存；重复取消保持幂等。重试已取消订单返回 cancelled，不重新下单。
- 数据库复合外键保证租户与关系一致，RLS 同时约束自动 SQL 与业务 Resolver SQL。
- 订单及库存没有自动写入 API，客户端无法绕过业务规则直接修改。客户 create/update 由数据库自动生成并仅向操作员开放；商品维护属于应用自己的管理权限边界。
- CDC 订阅推送最新状态，提交成功后才通知；它不提供事件历史。

## 组合与副本

`buildEndpoint` 同时用于启动、测试和副本装配。增加副本时业务 Resolver 无需修改，
所有副本连接相同业务库和共享安全状态。负载均衡、TLS 及滚动发布由部署环境管理。

```go
gateway, err := gql.NewGateway("admin",
    gql.Namespace{Name: "sales", EndpointID: "orders", Endpoint: ordersEndpoint},
)
```

组合后查询为 `{ sales { orders { total } } }`，突变为
`mutation { sales_placeOrder(...) { id } }`。突变保持 GraphQL 顶层顺序语义，因此使用前缀；
没有跨实例分布式事务。远程 HTTP 组合的完整签名、执行 pin 和共享 replay 示例见
`TestOrderRemoteComposition`；跨进程撤权与重放还由核心 GA 集成测试覆盖。

## 可复现验收

测试自动创建并销毁独立 PostgreSQL 容器，不清理开发数据库：

```bash
GOWORK=off go test . -count=1
GOWORK=off go test -race . -count=1
GOWORK=off ORDERS_SOAK_SECONDS=300 go test . -run TestOrderPopulatedHTTPWorkload -count=1 -v
```

覆盖业务闭环、幂等、回滚、越租户拒绝、只读权限、JWT、双实例防超卖、Gateway、CDC 和
10 万订单/10 万明细的真实 HTTP 混合负载。性能门槛是回归预算，不是所有部署的容量承诺。
生产使用独立凭据、TLS、受控迁移账号；应用账号无超级用户/BYPASSRLS，复制账号只读并具有 REPLICATION。
