# 教程：第一次查询和 Mutation

继续使用[上一章](01-first-engine.md)的订单服务和 operator JWT。在 `gql/examples/orders` 启动服务后，下面每个 JSON 代码块都可以作为请求体：

```bash
curl http://127.0.0.1:8080/graphql -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' --data-binary @request.json
```

将要执行的 JSON 保存为 `request.json`。这些代码块由 `TestOrderTutorialRequests` 直接读取并经过真实数据库和认证 HTTP handler 执行。

## 列表、过滤和排序

```json
{"query":"query Customers($limit:Int!,$offset:Int!) { customers(limit:$limit,offset:$offset,where:{name:{like:\"%Ali%\"}},sort:{id:ASC}) { items { id name } total } }","operationName":"Customers","variables":{"limit":20,"offset":0}}
```

`items` 是当前页，`total` 是满足过滤条件的总数。客户端传 `query` 即可执行；只有应用事先用 `LoadDocuments` 登记过命名操作时，才可以省略 `query`、只传 `operationName`。给查询起名不会自动完成登记。

## 游标分页

```json
{"query":"query Page($first:Int,$after:Cursor) { customers(first:$first,after:$after,sort:{id:ASC}) { items { id name } pageInfo { hasNext hasPrev start end } } }","operationName":"Page","variables":{"first":1,"after":null}}
```

下一页把 `pageInfo.end` 原样作为 `after`；反向分页使用 `last` 和 `before`。游标绑定租户、策略、排序和 Schema 版本，不能跨上下文复用。

## 自动生成的客户维护

```json
{"query":"mutation Create($input:CustomerCreateInput!) { createCustomer(input:$input) { id name } }","operationName":"Create","variables":{"input":{"name":"Tutorial Customer"}}}
```

`createCustomer` 直接返回客户对象，没有 `affected/items` 包装。更新使用返回的 id；下例更新初始化数据中的客户 1：

```json
{"query":"mutation { updateCustomer(id:1,input:{name:\"Alice Updated\"}) { id name } }"}
```

租户字段由可信上下文填充，不接受客户端覆盖。订单示例仅向 operator 开放客户创建/更新；没有开放客户删除或绕过业务命令的库存、订单写入。

## 业务命令和关联查询

```json
{"query":"mutation { placeOrder(requestId:\"tutorial-order\",customerId:1,items:[{productId:1,quantity:2}]) { id status } }"}
```

```json
{"query":"{ orders(limit:20) { items { id status customer { name } orderItems { quantity priceCents product { name } } } total } }"}
```

下单返回的订单 UUID 可传给 `cancelOrder(id: UUID)` 取消并回补库存。重复相同 requestId 不重复扣减，重复取消不重复回补。实现和完整断言见 [orders.go](../../examples/orders/orders.go) 与 [orders_test.go](../../examples/orders/orders_test.go)。

## 事务和订阅

单个 Engine 的 Mutation 顶层字段按顺序执行，共享请求事务；业务 Resolver 从 `ExecutionContextFromContext(ctx)` 取得执行视图，再用 `DB()` 或 `RequireDB()` 获取当前连接。跨 namespace 的 Mutation 不提供分布式事务。

订阅同样 POST 到 `/graphql`，增加 `Accept: text/event-stream`；完整命令见[订单 README](../../examples/orders/README.md)。它推送最新查询状态，JWT 到期时连接结束，不提供历史事件重放。

删除、upsert、嵌套关系写、聚合、全文搜索等更广的能力，请继续看[博客全能力示例](../../examples/README.md)。能力只在数据库约束、配置和权限允许时暴露。

---

[← 上一章](01-first-engine.md) · [文档首页](../README.md) · [下一步：数据库映射 →](../guides/database.md)
