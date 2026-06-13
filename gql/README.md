# gql — 数据库自动映射 GraphQL 引擎

参考 [graphjin](https://github.com/dosco/graphjin) 的设计：读取数据库元数据自动生成标准
GraphQL schema，请求编译为**单条 SQL**（LATERAL JOIN + JSONB 聚合）执行，任意深度的
关系嵌套都不会产生 N+1 查询。

## 架构

```
GraphQL 请求
  → gqlparser 解析/校验（schema 由 renderer 从元数据生成，或从文件加载）
  → 编译缓存命中? ──是──→ 取执行计划
        │否
  → Dialect(策略模式) 编译 → Plan{SQL, 参数槽位, resolver绑定}
  → 槽位填充变量 → 单条 SQL 执行
  → __root JSON 解包为 data（顶层 key = 字段别名）
  → Resolver 后处理（自定义字段，批量接口免 N+1）
  → 响应
```

| 组件 | 职责 |
|------|------|
| `metadata` | 四种加载器（pgsql/mysql/file/config）多源融合，自动推导关系并在虚拟字段挂载 join 元数据 |
| `protocol` | 元数据结构（Class/Field/Relation）与协议常量，方言唯一允许依赖的包 |
| `renderer` | 从元数据生成 GraphQL schema（Result/WhereInput/SortInput/CreateInput/UpdateInput） |
| `compiler` | 编译上下文（对象池、参数槽位、全局别名计数）与 Dialect 接口 |
| `compiler/pgsql` | PostgreSQL 方言：SELECT 单元化编译 + 变更 CTE |
| `executor` | HTTP 入口、计划 LRU 缓存、执行与结果组织、resolver 分发、持久化查询 |

## 快速开始

```go
import _ "github.com/ichaly/ideabase/gql/compiler/pgsql" // 空白导入自注册PostgreSQL方言

k, _ := std.NewKonfig()                    // 配置（可声明虚拟字段/关系/排除表等）
meta, _ := gql.NewMetadata(k, db)          // db: *gorm.DB，自动加载表结构与外键关系
compile, _ := gql.NewCompiler(meta, nil)   // nil：按驱动名从注册表自动路由方言
executor, _ := gql.NewExecutor(db, gql.NewRenderer(meta), meta, compile)

executor.Bind(app.Group(executor.Path()))  // fiber v3：POST查询变更 + GET订阅WebSocket升级
```

## 查询能力

- 过滤：`where: { name: { eq/ne/gt/ge/lt/le/in/like/iLike/regex/iRegex/is/hasKey... } }`，
  支持 `and/or/not` 任意组合；`id: X` 是主键等值的快捷方式
- 排序：`sort: { name: ASC, age: DESC_NULLS_LAST }`（单对象或列表均可）
- 分页：`limit/offset` + `total`（窗口函数一次查询同时取数与总数）；
  游标分页 `first/after`、`last/before` + `pageInfo{hasNext,hasPrev,start,end}`，
  keyset 语义性能恒定（排序键自动追加主键兜底；排序键应为非空列）；页大小
  支持变量（`first: $n`），同一查询文本一份计划适配任意页大小
- 统计：`userStats(where, groupBy, having, limit, offset)` 返回 `count` 与各列的
  sum/avg/min/max/countDistinct，选择驱动只算请求的聚合。`having` 对聚合值过滤
  （`having: { count: { gt: 10 }, score: { sum: { gt: 1000 } } }`），复用 where
  的操作符在数据库内 `HAVING` 过滤，不把多余分组传到应用层
- 全文搜索：实体声明搜索列后获得 `search: "关键词"` 参数，无显式 sort 时
  按相关度降序。**启动自动探测三档**：装有 pg_jieba/zhparser → tsvector
  真分词（自动发现其分词配置）；否则 pg_trgm 三元组（contrib 模块自动
  `CREATE EXTENSION`，官方镜像零额外部署，中文子串检索可用）；再否则
  ILIKE 降级。`search.mode/config` 可显式覆盖。中文建议配 GIN 索引：
  `CREATE INDEX ON posts USING gin (title gin_trgm_ops)`

  ```yaml
  metadata:
    classes:
      Post: { table: posts, search: [title, content] }
  ```
- 关系：多对一/一对多/多对多（中间表）/递归（parent/children）自动生成，
  嵌套关系字段同样支持 `where/sort/limit/offset`
- 递归全树：自关联实体自动生成 `descendants/ancestors(depth: Int)` 字段，
  递归 CTE 单查询全树遍历，depth 缺省 5（1~32，限深防爆炸）；建议 parent_id 建索引
- 去重：`distinct: ["列"]` 编译为 DISTINCT ON；Json 列支持
  `contains/containedIn`（jsonb @>/<@，可配 GIN 索引）
- 变更：`createX(input)` / `updateX(input, id|where)` / `deleteX(id|where)`，
  变更 CTE + 读回单条 SQL 原子完成；update/delete 强制要求条件
- 批量与 upsert：`createUsers(input: [..!]!)` 多行单条 INSERT（约束：参数总数
  受 PG 协议 65535 上限，万行级请分批）；`upsertUsers(input, on: ["email"])`（`on` 兼容单值写法 `on: "email"`）
  ON CONFLICT DO UPDATE，on 缺省主键
- 嵌套写入：输入中列表关系字段接受 `{connect:[ID!], disconnect:[ID!], create:[子CreateInput!]}`
  （connect/disconnect 挂接解除既有行，create 内联建新行并自动填外键），
  一对多改外键、多对多插删中间表，与主变更同语句原子；携带关系操作的
  更新必须按 id 定位。注意 PG 快照语义：同请求读回看不到关系变更，
  需后续查询确认（写入本身原子生效）
- GraphQL 变量：编译为参数槽位，同一查询文本的计划可缓存复用

## 自定义 Resolver

SQL 表达不了的字段逻辑用 Resolver 处理。配置声明虚拟字段：

```yaml
metadata:
  classes:
    User:
      table: users
      fields:
        greeting: { type: String, nullable: true, resolver: greet }
```

实现并注册（实现 `BatchResolver` 时列表整批一次调用，免 N+1）：

```go
type Greet struct{}
func (Greet) Name() string { return "greet" }
func (Greet) Resolve(ctx context.Context, source, args map[string]any) (any, error) {
    return "Hello, " + source["name"].(string), nil // 只能读取查询已选择的字段
}
executor.Register(Greet{})
```

> 列表场景下 `Resolve` 会被**并发调用**（有界并发），实现须线程安全；
> 有状态或需要共享资源的逻辑请实现 `BatchResolver`（整批单次调用，无并发约束）。

## 行级作用域（多租户 / 当前登录人）

按当前租户或登录人强制过滤数据。核心是：作用域值来自**服务端认证上下文**，
不是客户端查询参数，所以不进 schema、对客户端透明，**完整自省不受影响**。

实体声明作用域列与上下文键的对应：

```yaml
metadata:
  classes:
    Post: { table: posts, scope: [{ column: tenant_id, context: tenant }] }
    # 当前登录人:{ column: user_id, context: userId }；可同时声明多条，各注入一条 AND
```

认证中间件把值注入请求上下文（`gql.WithScope`）：

```go
func AuthMiddleware(c fiber.Ctx) error {
    claims := parseJWT(c.Get("Authorization"))      // 服务端解出当前租户/登录人
    ctx := gql.WithScope(c.Context(),
        map[string]any{"tenant": claims.Tenant, "userId": claims.UserId})
    c.SetContext(ctx)
    return c.Next()
}
```

编译期自动 AND 进 WHERE（作用域值执行期从上下文取，作为参数槽位，计划仍缓存）：

```sql
-- 客户端只写 { posts { items { title } } }，引擎强制注入：
SELECT ... FROM posts WHERE posts.tenant_id = $1   -- $1 = ctx 的 tenant
```

- 嵌套关系字段（读基表）同样注入；变更读回（读 CTE）跳过
- 客户端自己叠 `where: { tenantId: { eq: 99 } }` 只会 AND 出更窄的集合，绕不过
- 无作用域上下文时该参数为 `NULL`，匹配不到任何行（安全默认）
- 当前覆盖**查询读隔离**；变更（update/delete 的 WHERE、create 自动填列）的写隔离为后续

### 批量机制：resolver 如何不产生 N+1

关系嵌套的 N+1 由单条 SQL 根除；resolver 在 SQL 之外，靠**整结果集批量收集 +
一次调用**消除 N+1。三步：

1. **绑定收集**（编译期，`collectBindings`）：遍历查询 AST，为每个 resolver 字段
   记一条 `binding{Path, Field, Name}`。`Path` 是从 data 根到宿主对象的别名路径，
   数组层级（`items`、一对多关系）留到执行期展开；该字段在 SQL 编译期被跳过，
   保证 schema=能力=自省一致。
2. **宿主拍平**（执行期，`hosts`）：SQL 查完拿到完整结果树后，沿 `Path` 把这个
   resolver 要填充的**所有**宿主对象——无论分布在多少行、多少层嵌套里——一次性
   收集成一个扁平数组 `sources`。
3. **批量调用**（`resolve`）：实现了 `BatchResolver` 则 `ResolveBatch(sources)`
   **一次**处理全部 N 个宿主（内部可 `WHERE id IN (...)` 一把查完，N+1→1+1）；
   仅普通 `Resolver` 则退化为逐宿主调用，但走有界并发（8）兜底。

关键：批量是**跨整个结果集**的，不是每行一批——深层嵌套里的 resolver 字段也只
调用一次（e2e 有调用次数断言，微基准 `BenchmarkHosts` 固化拍平开销）。

## 订阅（CDC 驱动）

订阅基于 **WAL 逻辑复制（CDC）**：引擎维持一条复制连接（pgoutput 内置插件 +
临时复制槽，断开自动删除），解码到**表级变更**后只唤醒涉及该表的订阅重查，
结果指纹变化才推送。空闲零数据库负载，推送毫秒级。

```go
// 程序内订阅（channel API），首次立即推送当前结果
events, _ := executor.Subscribe(ctx, `subscription { users { items { name } total } }`, nil, "")
for reply := range events { ... } // ctx取消后通道关闭
```

HTTP 侧 `Bind` 已注册 GET 路由为 WebSocket 升级入口，
实现 [graphql-transport-ws](https://github.com/enisdenjo/graphql-ws/blob/master/PROTOCOL.md)
子协议（connection_init/ack、subscribe、next、complete、ping/pong），
可直接对接 Apollo Client / graphql-ws 客户端。

**部署要求**（官方 PG 镜像即可，无需扩展或自定义镜像）：

```yaml
services:
  postgres:
    image: postgres:16
    command: ["postgres", "-c", "wal_level=logical"]  # 唯一必须项
```

- 连接账号需 `REPLICATION` 权限（默认超级用户自带）；发布由引擎自动
  `CREATE PUBLICATION ideabase_cdc FOR ALL TABLES`，也可由 DBA 预建后配置
  `subscription.publication` 指定
- 复制连接缺省复用主连接 DSN，可用 `subscription.dsn` 单独指定
- 断线自动退避重连，重连后广播唤醒补偿期间可能错过的变更

## schema 与操作文档加载

- `schema.file` 配置后从文件加载 schema（生产推荐，启动更快且可人工裁剪）；
  未配置时由 renderer 现场生成并写入 `cfg/schema.graphql`
- `executor.LoadDocuments(dir)` 加载目录下 `.graphql` 操作文档（持久化查询），
  操作按名注册并预热编译缓存；HTTP 请求省略 `query` 仅传 `operationName` 即可执行

## 性能要点

- 任意深度嵌套 = 单条 SQL，无 N+1
- 编译计划 LRU 缓存（默认 512），key 为操作名+查询文本，变量不参与 key
- 编译上下文走 `sync.Pool`，热路径零反射
- 整体 `input` 变量的变更依赖变量内容，自动跳过缓存（volatile）
- 响应直通：无 resolver 时 DB 返回的 `__root` 字节经 `MarshalJSON` 直接拼入响应，
  跳过「解包成 map 再序列化」往返
- resolver 宿主拍平单遍直收、无 `interface{}` 装箱中间层

### 性能基准

两类基准，运行 `go test -bench=. -benchmem`：

- **引擎微基准**（`gql/bench_test.go`，无需数据库）：固化上述两处优化为可回归基线。
  Apple M1 Max 实测（50 行列表响应）：

  | 基准 | ns/op | allocs/op |
  |------|-------|-----------|
  | `BenchmarkReplyDirect`（直通拼接） | ~3.5K | 3 |
  | `BenchmarkReplyUnpack`（解包重序列化） | ~149K | 3243 |
  | `BenchmarkHosts`（64 宿主拍平） | ~0.6K | 11 |

  直通相对解包约 **40×**、分配降三个数量级——无 resolver 的查询（多数流量）走此路径。

- **端到端基准**（`gql/example/bench_test.go`，需 demo 库在运行）：复用 demo 实际装配，
  打真实 PostgreSQL，覆盖平铺查询、嵌套关系（单条 SQL 零 N+1）、batch resolver。
  `cd example && go test -bench=. -benchmem -run=^$`。

## 扩展新数据库（纯新增，零修改）

所有数据库相关能力都走自注册（`database/sql` 驱动同款模式），
将来支持 MySQL 只需**新增**以下代码，不修改任何现有文件：

1. **方言**：新建 `compiler/mysql` 包实现 `compiler.Dialect` 的 5 个方法
   （契约见接口注释：单行单列 __root JSON、参数槽位、MarkTable 等），
   `init` 中 `compiler.Register(NewDialect())`——空白导入即生效，
   `NewCompiler(meta, nil)` 自动按驱动名路由；已知驱动未注册方言会明确报错
2. **订阅唤醒源**：新增文件实现 `notifier` 接口（binlog 监听）并
   `registerNotifier("mysql", 工厂)`——执行器按 `db.Name()` 自动选取
3. **全文搜索探测**：新增文件实现探测函数并 `registerSearchDetector("mysql", 探测)`
   ——执行器按 `db.Name()` 自动选取；未注册的驱动降级为 ilike
4. **元数据**：`MysqlLoader` 已就绪，按驱动自动启用，无需任何动作

当前未注册 MySQL 的任何实现：连 MySQL 时编译器报「没有注册对应的SQL
方言实现」，订阅报「没有注册CDC唤醒源」，搜索降级 ilike，不会静默出错。

## 契约一致性

schema、能力、自省三者严格一致，没有任何方向的偏差：

- **没有隐藏能力**：所有请求先经 schema 校验（gqlparser），schema 没有的字段/参数直接报错——不存在 graphjin 那种"文档不展示但提交能用"的隐含关键字
- **没有虚假展示**：未实现的能力（如尚未支持的 MySQL 方言）不渲染进 schema，文档里看到的就是能用的
- **自省完整**：按客户端查询形状投影（支持别名/fragment），GraphiQL、Apollo codegen 的标准 IntrospectionQuery 直接对接（有测试覆盖）；`__typename` 在根/Result/实体各层级编译为类型名字面量，Apollo 客户端缓存正常工作

## 当前限制

- MySQL 方言未实现（扩展方式见上节；已知驱动未注册方言会明确报错，不会静默回退）
- 同一 mutation 内不能两次变更同一张表（变更 CTE 同名限制）

设计细节见 [`../doc/gql-rework-plan.md`](../doc/gql-rework-plan.md) 与
[`../doc/pgsql-template-design.md`](../doc/pgsql-template-design.md)。
