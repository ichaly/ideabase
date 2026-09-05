# GQL 全能力示例

这个目录用一个多租户博客模型覆盖引擎公开能力。`coverage.json` 是机器可校验的能力
清单；根目录 `TestExamplesCoverDocumentedCapabilities` 保证每项能力都有实际代码、SQL、
GraphQL 文档或测试作为证据，避免文档声称支持但示例缺失。

## 快速开始

```bash
cd gql/examples
docker compose up -d --wait
go run .                         # 默认监听 :8080
# 端口冲突时：DEMO_HTTP_ADDR=:18080 go run .
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
| Query/Mutation 根 Resolver | `serverInfo/echo` | `main.go`, `operations/public/extensions.graphql` |
| Resolver middleware | `User.greeting` 的 `[wrapped]` 前缀 | `main.go` |
| ID 与自定义 Codec、Matcher、Baser | `id`、所有 `name` 字段 | `UpperCodec`, `buildEngine` |
| 自增、UUID、雪花、自定义主键 | `GeneratedPrimaryKeys` | `main.go`, `schema.sql` |
| 按键批量 Resolver | `User.reputation` | `main.go`, `ResolverAndRemote` |
| 行级作用域 | `Post.tenant_id ↔ tenant` | `main.go`, `TenantScopedPosts` |
| 持久化查询与缓存预热 | 所有命名 operation | `queries/*.graphql` |
| CDC SSE 订阅 | `subscription { users { total } }` | `docker-compose.yml` |
| 标准自省 | `StandardIntrospection` | `operations/dev/introspection.graphql` |
| 安全开关 | introspection/persisted-only/max-depth | `config.production.yml` |
| 字节响应快路径 | `Engine.Execute` | `demo_test.go` |
| Gateway HTTP 组合 | 同一 `/graphql` 挂载组合后的服务 | `http_test.go` |
| TenantGateway 路由 | `/graphql` 的租户 1/2 与未知租户拒绝 | `main.go`, `http_test.go` |
| 控制面健康/Manifest | `/health`, `/manifest` 及未授权拒绝 | `main.go`, `http_test.go` |
| Effective Schema | `/effective-schema` delegation 验证 | `main.go`, `http_test.go` |
| Transport Descriptor | `/.well-known/gql-gateway` | `main.go`, `http_test.go` |
| 权限拒绝 | `X-Demo-Deny` 真实错误响应 | `main.go`, `http_test.go` |
| 多副本共享状态 | 两个 PostgreSQL provider/replay store 观察同一撤权与 replay 事实源 | `http_test.go`, `docker-compose.yml` |
| Remote → Gateway HTTP | 独立 child 进程的 manifest/effective schema/执行/health/撤权链路 | `scripts/test-demo.sh` |
| 性能基线与门禁 | 真实 PostgreSQL 嵌套单 SQL、8 并发、吞吐/P99 预算；另有 benchmark | `performance_test.go`, `bench_test.go` |

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
GQL '{"operationName":"GeneratedPrimaryKeys"}'
```

`update/delete` 没有 `id` 或 `where` 会在编译期拒绝。嵌套写入使用同一条 SQL 原子提交；
受 PostgreSQL 语句快照影响，关系变更请在后续查询确认。

## Resolver 与 Codec

`main.go` 展示五种扩展方式：

- `NewResolver("User", ...)`：实体字段，强类型参数。
- `NewBatch("User", ...)`：整个结果集一次调用，避免 N+1。
- `NewResolver("Query"/"Mutation", ...)`：复杂根查询或突变。
- `Wrap("User.greeting", ...)`：中间件增强已有 Resolver。
- `NewLoader(...)`：按宿主键批量请求外部数据并回填。

`NewIdCodec()` 同时演示 ID 入参还原与出参编码；`UpperCodec` 演示自定义标量、字段
认领、过滤器复用和双向边界转换，数据库仍保存原始值。未选择 Resolver
字段时不会触发解包；选择 `greeting/sign/reputation` 时只解包命中分支。

主键策略通过实体的 `id-generator` 声明：省略或 `database` 使用数据库自增/default
（UUID列用 `DEFAULT gen_random_uuid()`），`snowflake` 是内置生成器，其他名称由
`WithIDGenerator` 一次注册后复用。

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

Docker 已设置 `wal_level=logical`。向 `/graphql` POST 标准 GraphQL JSON，
并以 `Accept: text/event-stream` 接收 SSE：

```bash
curl -N http://localhost:8080/graphql -H 'Accept: text/event-stream' \
  -H 'Content-Type: application/json' \
  -d '{"query":"subscription { users { total } }"}'
```

首次推送当前值；相关表发生变化后 CDC 唤醒并仅在结果变化时推送。共享同构订阅只执行
一次重查，慢消费者只保留最新状态。

## 生产配置与验证

`config.production.yml` 演示关闭自省、仅允许持久化操作、默认分页和深度限制；Demo 默认
使用动态 schema，便于观察注册即声明的 Resolver 字段。

```bash
docker compose down --volumes --remove-orphans
docker compose up -d --wait
go test -count=1                         # 必须连接真实 PostgreSQL，不可用时直接失败
cd .. && go test . -run TestExamplesCoverDocumentedCapabilities -count=1
cd examples && go test -run '^$' -bench . -benchmem
./scripts/test-demo.sh                   # 重置数据库、全能力测试、真实HTTP启动访问
```

`go test` 会执行 `queries/` 和 `operations/` 下的每一个持久化 operation，并通过真实 HTTP
入口验证持久化查询和 SSE 首个事件。`test-demo.sh` 还会启动两个独立 test binary，连接同一
PostgreSQL，验证 replay 一次消费、撤权传播、活动副本撤权和 revision 栅栏。数据库不可达、
operation 未加载、GraphQL errors、HTTP 启动失败或多进程门禁失败都会使命令失败，不会以跳过
方式报告成功。

性能门禁在同一套 Demo PostgreSQL 上验证：嵌套关系查询必须保持单条 SQL；8 个并发 worker
执行 64 个 warm 请求，错误率必须为零，吞吐至少 50 req/s，P99 不超过 500ms。详细的
ns/op、allocs/op 和不同查询形状使用 `go test -bench=. -benchmem -run=^$` 获取。

在 Apple M1 Max、本地 Docker PostgreSQL 16 的一次基线采样中：平铺查询约 4.36–4.49ms /
请求、嵌套关系约 4.64–4.89ms / 请求、Batch Resolver 约 4.39–4.61ms / 请求；每请求约
2.78–2.81MB、36K 次分配。该数据用于同机 base/head 回归比较，不作为生产硬件 SLO。

默认 Demo 在一个 `/graphql` 入口执行查询、突变和 SSE 订阅，并统一经过租户路由。
控制面路径用于实例组合与运维，不是额外业务 API。`NewLoader` 与 `NewBatch` 都返回 Resolver，没有独立 Remote 字段执行器。
