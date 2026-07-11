# GQL 全能力示例

这个目录用一个多租户博客模型覆盖引擎公开能力。`coverage.json` 是机器可校验的能力
清单；根目录 `TestExamplesCoverDocumentedCapabilities` 保证每项能力都有实际代码、SQL、
GraphQL 文档或测试作为证据，避免文档声称支持但示例缺失。

## 快速开始

```bash
cd gql/examples
docker compose up -d
go run .
```

服务地址为 `http://localhost:8080/graphql`。Demo 默认注入租户 `1`，可用
`X-Demo-Tenant: 2` 切换；生产环境必须从认证声明中取租户，不能信任 Header。

```bash
GQL() { curl -s http://localhost:8080/graphql \
  -H 'Content-Type: application/json' -H 'X-Demo-Tenant: 1' -d "$1"; }

GQL '{"operationName":"PostList","variables":{"kw":"数据库"}}'
GQL '{"query":"{ users { items { id name email greeting sign reputation { level title } } } }"}'
GQL '{"query":"{ serverInfo { name version } }"}'
GQL '{"query":"mutation { echo(message: \"hello\") }"}'
```

启动时 `queries/*.graphql` 全部经过 schema 解析、校验并预热计划缓存，因此示例文档本身
也是可执行契约，而不是仅供阅读的片段。

## 能力覆盖矩阵

| 能力 | 可运行样例 | 实现位置 |
|---|---|---|
| 标准 create/update/delete | `CrudCreate/CrudUpdate/CrudDelete` | `queries/mutations.graphql` |
| 批量新增、upsert | `BatchCreate/UpsertByUnique` | `queries/mutations.graphql` |
| 嵌套 create/connect/disconnect | `NestedCreate/NestedConnectDisconnect` | `queries/mutations.graphql` |
| and/or/not 与全部常用操作符 | `FilterSortOffset` | `queries/queries.graphql` |
| 多字段排序、NULL 顺序 | `FilterSortOffset` | `queries/queries.graphql` |
| limit/offset/total | `FilterSortOffset` | `queries/queries.graphql` |
| first/after/last/before | `ForwardCursorPage/BackwardCursorPage` | `queries/queries.graphql` |
| count、groupBy、having、列聚合 | `AggregateHaving` | `queries/queries.graphql` |
| 中文全文搜索与降级探测 | `PostList` | `queries/blog.graphql`, `schema.sql` |
| 多对一、一对多、多对多 | `JsonDistinctRelations` | `queries/queries.graphql`, `schema.sql` |
| 自关联 descendants/ancestors | `RecursiveComments` | `queries/queries.graphql`, `schema.sql` |
| distinct、JSON contains/hasKey | `JsonDistinctRelations` | `queries/queries.graphql` |
| variables、alias、fragment、typename | `FilterSortOffset/PostList` | `queries/*.graphql` |
| 普通字段 Resolver | `greeting` | `main.go`, `ResolverAndRemote` |
| Batch Resolver | `sign` | `main.go`, `ResolverAndRemote` |
| Query/Mutation 根 Resolver | `serverInfo/echo` | `main.go`, `extensions.graphql` |
| Resolver middleware | `User.greeting` 的 `[wrapped]` 前缀 | `main.go` |
| ID 与自定义 Codec、Matcher、Baser | `id`、所有 `name` 字段 | `UpperCodec`, `buildExecutor` |
| Remote Join | `User.reputation` | `main.go`, `ResolverAndRemote` |
| 行级作用域 | `Post.tenant_id ↔ tenant` | `main.go`, `TenantScopedPosts` |
| 持久化查询与缓存预热 | 所有命名 operation | `queries/*.graphql` |
| CDC WebSocket 订阅 | `subscription { users { total } }` | `docker-compose.yml` |
| 标准自省 | `StandardIntrospection` | `operations/extensions.graphql` |
| 安全开关 | introspection/persisted-only/max-depth | `config.production.yml` |
| 字节响应快路径 | `Executor.Execute` | `demo_test.go` |

JSON 形式的同一矩阵见 `coverage.json`，增加或删除 README 公开能力时应同步更新。

## 查询示例

过滤、排序和 offset 分页：

```bash
GQL '{"operationName":"FilterSortOffset","variables":{"limit":2,"offset":0}}'
```

双向游标分页：

```bash
GQL '{"operationName":"ForwardCursorPage","variables":{"first":2,"after":null}}'
# 将 pageInfo.end 作为下一次 after；反向使用 last + before。
```

关系、JSON 和去重：

```bash
GQL '{"operationName":"JsonDistinctRelations"}'
GQL '{"operationName":"RecursiveComments"}'
GQL '{"operationName":"AggregateHaving"}'
```

## 变更示例

```bash
GQL '{"operationName":"CrudCreate","variables":{"name":"赵六","email":"zhao@demo.dev"}}'
GQL '{"operationName":"BatchCreate"}'
GQL '{"operationName":"UpsertByUnique"}'
GQL '{"operationName":"NestedCreate"}'
GQL '{"operationName":"NestedConnectDisconnect"}'
```

`update/delete` 没有 `id` 或 `where` 会在编译期拒绝。嵌套写入使用同一条 SQL 原子提交；
受 PostgreSQL 语句快照影响，关系变更请在后续查询确认。

## Resolver、Remote 与 Codec

`main.go` 展示五种扩展方式：

- `NewResolver("User", ...)`：实体字段，强类型参数。
- `NewBatch("User", ...)`：整个结果集一次调用，避免 N+1。
- `NewResolver("Query"/"Mutation", ...)`：复杂根查询或突变。
- `Wrap("User.greeting", ...)`：中间件增强已有 Resolver。
- `NewRemote(...)`：按宿主键批量请求外部数据并回填。

`NewIdCodec()` 同时演示 ID 入参还原与出参编码；`UpperCodec` 演示自定义标量、字段
认领、过滤器复用和双向边界转换，数据库仍保存原始值。未选择 Resolver
字段时不会触发解包；选择 `greeting/sign/reputation` 时只解包命中分支。

## 多租户作用域

`posts.tenant_id` 不进入 CreateInput/UpdateInput。编译器从 `gql.WithScope` 注入：

- 查询、关系和递归自动追加 `tenant_id = $n`；
- create/upsert 自动填充；
- update/delete 只能影响当前租户；
- 没有作用域值时安全地匹配不到任何记录。

```bash
curl -s http://localhost:8080/graphql -H 'Content-Type: application/json' \
  -H 'X-Demo-Tenant: 2' -d '{"operationName":"TenantScopedPosts"}'
```

## 订阅

Docker 已设置 `wal_level=logical`。使用 `graphql-transport-ws`：

```json
{"type":"connection_init"}
{"id":"1","type":"subscribe","payload":{"query":"subscription { users { total } }"}}
```

首次推送当前值；相关表发生变化后 CDC 唤醒并仅在结果变化时推送。共享同构订阅只执行
一次重查，慢消费者只保留最新状态。

## 生产配置与验证

`config.production.yml` 演示关闭自省、仅允许持久化操作、默认分页和深度限制；Demo 默认
使用动态 schema，便于观察注册即声明的 Resolver/Remote 字段。

```bash
go test ./examples -count=1
go test . -run TestExamplesCoverDocumentedCapabilities -count=1
go test ./... -count=1
go test ./examples -run '^$' -bench . -benchmem  # 数据库可用时
```
