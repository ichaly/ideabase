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
  → 槽位填充变量（codec标量入参已按类型还原） → 单条 SQL 执行
  → codec出参转换（对__root字节流式改写，无codec零开销）
  → 无 Resolver：__root JSON 字节直接返回
  → 有 Resolver：仅解包命中分支并执行后处理（批量接口免 N+1）
  → GraphQL JSON 字节响应
```

| 组件 | 职责 |
|------|------|
| `metadata` | 四种加载器（pgsql/mysql/file/config）多源融合，自动推导关系并在虚拟字段挂载 join 元数据 |
| `protocol` | 元数据结构（Class/Field/Relation/Constraint/Routine）与协议常量，方言唯一允许依赖的包 |
| `renderer` | 从元数据生成 GraphQL schema（Result/WhereInput/SortInput/CreateInput/UpdateInput） |
| `compiler` | 编译上下文（对象池、参数槽位、全局别名计数）与 Dialect 接口 |
| `compiler/pgsql` | PostgreSQL 方言：SELECT 单元化编译 + 变更 CTE |
| `executor` | HTTP 入口、计划 LRU 缓存、执行与结果组织、Resolver/Remote 分发、codec 出入参转换、持久化查询 |
| `Snapshot` | Build 后不可变的 SDL、参数、Handler、Planner、授权 action、resource、cost 与稳定指纹 |

## 快速开始

```go
import _ "github.com/ichaly/ideabase/gql/compiler/pgsql" // 空白导入自注册PostgreSQL方言

k, _ := std.NewKonfig()         // 配置中为生产实体声明 permissions
builder := gql.New(k, db)       // db: *gorm.DB，自动加载表结构与外键关系
builder.LoadDocuments("queries")
engine, err := builder.Build()  // 构建一次 capability snapshot，随后冻结
if err != nil { log.Fatal(err) }

engine.Bind(app.Group(engine.Path()))  // fiber v3：POST查询变更 + GET订阅WebSocket升级

// 非HTTP场景使用同一字节快路径；GraphQL字段错误位于响应体errors中
body, err := engine.Execute(ctx, `query { users { items { id name } } }`, nil)

snapshot, _ := engine.Snapshot()
fmt.Println(snapshot.Fingerprint(), snapshot.Capabilities())
```

Builder 是唯一装配入口。每个 Engine 必须通过 `Builder.Policy` 或 metadata
`permissions` 获得唯一 PolicyProvider；两者冲突或两者都缺失时 Build 失败并保持
fail-closed。Build 后 Engine 只提供执行、快照、健康检查和订阅接口。

### 独立服务与命名空间组合

`Engine` 和 `Gateway` 实现同一个传输无关执行契约。标准库 HTTP、SSE、manifest 与
健康检查只是薄适配器，因此嵌入 Fiber、Gin、gRPC 或命令行时无需改变执行内核：

```go
mux.Handle("/graphql", transport.NewHandler(engine))
mux.Handle("/graphql/stream", transport.NewSubscriptionHandler(engine)) // 仅在有 CDC 时挂载
mux.Handle("/gql/manifest", transport.NewManifestHandler("accounts", engine))
mux.Handle("/health/gql", transport.NewHealthHandler(engine))

gateway, err := gql.NewGateway("public-api",
    gql.Namespace{Name: "accounts", EndpointID: "accounts", Endpoint: accounts},
    gql.Namespace{Name: "orders", EndpointID: "orders", Endpoint: orders},
)
```

组合后的 Query 以 namespace 对象呈现；Mutation 使用带 namespace 的顶层字段并保持
GraphQL 规定的串行顺序。不同 namespace Query 并行，同 namespace 选择只下发一次。
child 的 object/input/enum/interface/union 会确定性改名，scalar/directive 仅在契约一致时
共享；返回值还会按公共 Schema 重新完成，额外字段、错误路径和 `__typename` 不会泄漏
child 内部名字。`Gateway.Refresh` 只有在完整新拓扑校验成功后才原子换代，失败继续服务
旧 snapshot；执行中的请求固定使用启动时的 child fingerprint。

远程组合使用 `transport.NewRemoteEndpoint`，只接受启动时配置的 HTTPS 地址，禁止跳转，
并校验 endpoint identity、manifest、snapshot fingerprint、响应大小和返回类型。身份通过
短期签名 delegation envelope 传播，child 仍必须按自己的 policy 再授权；客户端伪造
tenant/role header 不构成可信身份。

## 查询能力

- 过滤：`where: { name: { eq/ne/gt/ge/lt/le/in/like/iLike/regex/iRegex/is/hasKey... } }`，
  支持 `and/or/not` 任意组合；单主键用 `id: X` 快捷定位，复合主键由自省生成
  实体专属 `XKeyInput`，例如 `orders(key: {tenantId: 7, code: "A"})`
- 排序：`sort: { name: ASC, age: DESC_NULLS_LAST }`（单对象或列表均可）
- 分页：`limit/offset` + `total`（窗口函数一次查询同时取数与总数）；
  游标分页 `first/after`、`last/before` + `pageInfo{hasNext,hasPrev,start,end}`，
  keyset 语义性能恒定（排序键自动追加主键兜底；排序键应为非空列）；页大小
  支持变量（`first: $n`），同一查询文本一份计划适配任意页大小。无显式 limit 的
  列表自动注入 `schema.default-limit`（缺省 10，设 0 关闭）防无界全表扫描；
  变更读回、统计分组与递归全树不受此限制。选择集嵌套深度超过
  `schema.max-depth`（缺省 20，设 0 关闭）在执行前拒绝。可执行文档同时受
  `schema.max-tokens`（缺省 15000，设 0 不限制）保护；`schema.max-cost`（缺省 0，
  即只通过 `OperationCost` 观测）可启用请求成本预算。成本消费规范化后的
  选择树，按分页上界和批量 `input` 长度乘算；无可证明上界的列表在预算模式下fail closed
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
- 去重：`distinct: [field]` 使用实体专属枚举并编译为 DISTINCT ON；Json 列支持
  `contains/containedIn`（jsonb @>/<@，可配 GIN 索引）
- 变更：`createX(input)` / `updateX(input, id|key|where)` / `deleteX(id|key|where)`，
  变更 CTE + 读回单条 SQL 原子完成；update/delete 强制要求条件
- 主键策略：实体 `id-generator` 支持 `database`（缺省，数据库自增/UUID default）、
  `snowflake` 与 `WithIDGenerator` 注册的自定义策略；生成发生在缓存计划执行期，
  批量和嵌套创建逐行生成且不重复
- 批量与 upsert：`createUsers(input: [..!]!)` 多行单条 INSERT（约束：参数总数
  受 PG 协议 65535 上限，万行级请分批）；`on` 是由真实 primary/unique 约束生成的
  `UserConflictTarget` 枚举，例如 `upsertUsers(input: [...], on: UNIQUE_1)`。
  partial/expression/deferrable unique 不会被误报为通用冲突目标；存在主键时 `on` 可省略
- 嵌套写入：输入中列表关系字段接受 `{connect:[ID!], disconnect:[ID!], create:[子CreateInput!]}`
  （connect/disconnect 挂接解除既有行，create 内联建新行并自动填外键），
  一对多改外键、多对多插删中间表，与主变更同语句原子；携带关系操作的
  更新必须按主键定位。注意 PG 快照语义：同请求读回看不到关系变更，
  需后续查询确认（写入本身原子生效）
- GraphQL 变量：编译为参数槽位，同一查询文本的计划可缓存复用

### PostgreSQL catalog 契约

引擎直接读取 `pg_catalog`，一次构建保留列序、default、identity/generated、enum/domain、
array、PK/unique/FK/check/exclusion、partial/expression/include index、table/view/materialized
view/partitioned/foreign table，以及重载 function/procedure 的结构化参数和返回事实。

- `NOT NULL DEFAULT` 在 `CreateInput` 中可省略；identity/generated 不进入写输入
- 数据库 enum 只有在类型名和值都能无损表示为 GraphQL Name 时才生成 enum，否则构建失败，
  可用显式 type mapping 裁决，绝不静默改值
- PostgreSQL array 以真实 GraphQL list 暴露并可参数化写入；尚未实现的数组过滤不会出现在自省
- view/materialized view/foreign table 默认只读；没有可执行 Mutation 就不生成 Mutation 字段
- 复合主键保留每列原生类型并生成 typed `KeyInput`；当前不支持的复合关系嵌套写入不会出现在输入类型
- `CatalogFingerprint()` 忽略可信 tenant schema 标识符但包含真实结构，用于判断多租户 catalog
  能否共享 canonical Schema/logical plan；SQL 始终使用配置中的 schema 限定表名

```go
meta, err := gql.NewMetadata(k, db,
    gql.WithIDGenerator("virtual", func() (any, error) { return nextVirtualID(), nil }),
)
```

```yaml
metadata:
  classes:
    Order: { table: orders, id-generator: snowflake }
    Token: { table: tokens, id-generator: database } # UUID列使用DEFAULT gen_random_uuid()
    User:  { table: users, id-generator: database }
```

## 自定义 Resolver

SQL 表达不了的字段逻辑用 Resolver 处理。**注册即声明**：字段挂载进宿主实体、
参数与字段类型反射自函数签名，不写 yaml：

```go
// source 默认只有客户端选择的列；Requires 声明业务依赖，planner 内部补投影并在响应前剥除。
// args 及其嵌套 struct 都生成强类型、自省可见的 GraphQL input。
builder.Register(gql.NewResolver("User", "hello", "问候语",
    func(ctx context.Context, source gql.Source, args struct {
        Lang string `json:"lang" validate:"omitempty,oneof=zh en"`
    }) (string, error) {
        return greet(args.Lang, source["name"]), nil
    }, gql.Requires("name"), gql.Timeout(200*time.Millisecond)))

// 批量版：整结果集一次调用免 N+1，返回值与 sources 等长对位
builder.Register(gql.NewBatch("User", "level", "等级",
    func(ctx context.Context, sources []gql.Source, _ struct{}) ([]int, error) { ... }))
```

> `NewResolver` 的函数在列表场景下会被**并发调用**（有界并发），实现须线程安全；
> 有状态或需要共享资源的逻辑用 `NewBatch`（整批单次调用，无并发约束）。
> 要完全控制时直接实现 `Resolver`/`BatchResolver` 接口。

## 自定义根字段 Resolver

查询、突变和实体字段使用同一个泛型 Resolver 抽象。数据库元数据生成的 CRUD
仍直接编译为单 SQL；只有复杂业务字段进入 Resolver 分发：

```go
builder.Register(gql.NewResolver("Mutation", "botSave", "注册闭环建号",
    func(ctx context.Context, _ gql.Root, req CreateReq) (*ent.Profile, error) {
        return svc.Create(ctx, req)
    }, gql.Result("BotProfile"), gql.Cost(8)))
```

`Cost`、`Requires`、`Timeout`、`MaxBatch` 与显式 `RetrySafe(idempotencyKey)` 都会进入
不可变 Snapshot。命名的嵌套输入/输出 struct 自动生成结构化 SDL，`Extra` 仅保留为兼容逃生口；
`engine.OperationCost(query, operationName, variables)` 使用与真实执行相同的验证、
coercion、directive 和选择集合并路径，但不受当前预算上限影响。

Mutation 根 Resolver 会在引擎事务中执行；业务代码统一从 `gql.DB(ctx)` 或
`gql.RequireDB(ctx)` 取得 request-scoped 连接，它会优先返回当前事务。需要 identity、
selection、deadline 和 snapshot fingerprint 时使用 `gql.ExecutionContextFromContext(ctx)`；
Query、无数据库执行器或事务外调用返回 `nil`。直接捕获全局 `*gorm.DB` 的旧写法无法
自动加入引擎事务，需要把事务连接继续传给 service/repository。

`Register` 新增字段；构造时传 `gql.Existing()` 后用 `Replace` 显式覆盖 schema 已有字段；
`Wrap` 用强类型中间件增强已注册 Resolver。自定义根字段可与默认数据库字段混排；
单根默认 CRUD 保持原单 SQL 快路径，多根 Mutation 按文本顺序在同一事务中执行。

- 类型映射：`std.Id`→ID、`time.Time`→DateTime、切片→列表（`[]std.Id`→`[ID!]`）、
  map/匿名 struct→Json、命名嵌套 struct→结构化类型；`validate:"required"` 渲染非空 `!`，
  `doc` tag 作字段文档
- 返回类型名匹配元数据实体 → 触发回查补全（返回实体结构体自动取 Id，等价于返回 id）；
  标量/map/切片 → 直接输出；`gql.Extra(sdl)` 可附加辅助 input/type 声明
- 回查补全时 resolver/关系/codec 全部生效；要完全控制 schema
  时直接实现 `Resolver` 接口

## 标量编解码器（Codec）

为一个 GraphQL 标量类型声明请求边界的双向转换（ID 加解密、手机号脱敏等）。
数据库任何场景（含 jsonb）永远存原始值，转换只发生在出入参：出参在取数出口对
数据库直通字节做**单遍流式改写**（编译期从选择集算出路径树随计划缓存，无命中
零分配返回原字节）；入参按变量声明/字面量类型还原，递归进入 input 对象。

```go
type Codec interface {
    Name() string                // 绑定的标量类型名，如 ID、Phone
    Encode(token []byte) []byte  // 出参：JSON token（数字或含引号字符串）→ 替换token；nil=原样
    Decode(value any) any        // 入参：线上值 → 数据库值；原样返回=不处理
}
```

两个可选扩展接口：

- `Matcher`——`Match(class, field) bool` 在元数据定型期主动认领字段（把命中字段
  的类型改写为本标量）；字段级配置显式指定类型时豁免认领（配置是最终裁决）
- `Baser`——`Base() string` 声明过滤器操作符集的借用来源，缺省借用 String

主键与外键实列经元数据定型期的结构推导恒定映射为 ID 标量（零声明、与 codec 无关）。
所有 codec 统一经 `WithCodecs` 注册（认领需改写字段类型，须在元数据构建期传入）：

```go
// 需要ID加解密就注册内置 NewIdCodec()，不注册则ID保持数字；业务codec同列追加
meta, _ := gql.NewMetadata(k, db, gql.WithCodecs(gql.NewIdCodec(), PhoneCodec{}))
```

内置 ID codec：出参编码为 sqids shortId（`~`前缀，与 REST 通道共用 std.Id 配置），
入参兼容 shortId / 十进制字符串 / 数字；`*_by` 整型审计列（无外键约束）由其
Match 认领为 ID。注册同名 `"ID"` codec 即可整体替换为自定义实现（换算法/字母表）。

自定义标量自动获得 `scalar X` 声明与过滤器类型；同名 codec 后注册者生效（可覆盖
内置 ID 实现）。注意 Encode 收到的是原始 JSON token 字节：字符串 token 含引号与
原始转义，返回值须是合法 JSON token。

## 远程关系（Remote Join）

把外部服务（REST/gRPC/另一个 GraphQL）声明为图里的关系字段，客户端一次查询同时
拿到数据库数据与远程数据，拼接由引擎完成。**注册即声明**：关系字段与虚拟类型
全部反射自函数签名（Go 类型名即 GraphQL 类型名，json 标签定字段名），不写 yaml：

```go
// Profile 虚拟类型：只有类型定义，不生成查询根/过滤/排序
type Profile struct {
    Level string `json:"level" doc:"等级"`
}

// "id" 为宿主键字段；Fetch 一次收到本批去重后的键集合，天然免 N+1
builder.Register(gql.NewRemote("User", "profile", "用户画像", "id",
    func(ctx context.Context, keys []any) (map[any]Profile, error) {
        // 批量调用外部服务，返回 键→值 映射；超时用 ctx 自行控制
    }))
```

执行语义：编译期自动把 key 列以内部别名补进投影（不受 codec 转换影响，回填后
剥除，响应形状严格等于选择集）；Fetch 失败时字段整体置 null 并记录告警，
不中断主查询；键未命中的行该字段为 null。变更读回的选择集同样生效。
多个远程共享同一返回类型时，虚拟类型 SDL 按文本去重。

> 诚实边界：远程字段不支持 where/sort 下推（数据不在库里），虚拟类型不渲染任何
> 查询面——schema 里看不到的就是不能用的。要完全控制时直接实现 `Remote` 接口。

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

读写全覆盖：

- **查询**：根字段、任意深度嵌套关系、递归全树（递归 CTE 起始层+步进层）都注入
- **变更**：`update/delete` 的 WHERE 强制 AND 作用域（只能改本租户/属主的行）；
  `create/upsert`（含嵌套创建）自动填作用域列为上下文值（防越租户创建）
- 作用域列**不进** CreateInput/UpdateInput（服务端强制填，客户端碰不到）

其他保证：

- 客户端自己叠 `where: { ... }` 只会 AND 出更窄的集合，绕不过
- 无作用域上下文时该参数为 `NULL`，匹配/影响不到任何行（安全默认）
- 变更读回（读 CTE）跳过注入，不误过滤刚写入的行
- 作用域值是参数（不影响 SQL 文本），同查询不同租户共享一份计划

## 数据治理（软删除、审计与角色权限）

治理规则仍编译进参数化 SQL；角色和上下文值不进入计划缓存键。同一查询文本在不同
角色间复用同一计划，未配置 `permissions` 与 `operations` 的实体保持原有全开放行为。

```yaml
metadata:
  classes:
    Post:
      table: posts
	  operations: [select, aggregate, insert, update, delete, restore, purge]
      soft_delete: { column: deleted_at }
      where:
        - { column: enabled, operator: eq, value: true }
      fields:
        createdBy: { column: created_by, read_only: true, insert_preset: { context: userId } }
        updatedBy: { column: updated_by, read_only: true, update_preset: { context: userId } }
      permissions:
        editor:
          select: [id, title, createdBy, updatedBy]
          insert: [title]
          update: [title]
          delete: true
	      restore: true
	      purge: true
          aggregate: true
          filter:
            - { column: tenant_id, operator: eq, context: tenant }
          check:
            - { column: tenant_id, operator: eq, context: tenant }
```

认证层注入作用域与完整身份；角色随服务端签发的 Identity 传递，不能由 GraphQL variables 伪造：

```go
ctx := gql.WithScope(ctx, map[string]any{
    "tenant": claims.Tenant,
    "userId": claims.UserId,
})
identity, _ := gql.NewIdentity(gql.IdentityInput{
    Tenant: claims.Tenant, Subject: claims.Subject, Roles: claims.Roles,
})
ctx = gql.WithIdentity(ctx, identity)
```

规则语义：

- `operations` 为空兼容旧行为；配置后只渲染并执行列出的能力
- 存在约定列 `deleted_at` 时自动启用软删，也可用 `soft_delete` 显式指定；查询的
  `deletion: ACTIVE | WITH_DELETED | ONLY_DELETED` 完整出现在自省中并受字段权限控制
- delete 标记当前活动行，restore 只恢复已删除行；显式声明并授权 `purge` 后才暴露物理删除
- 约定的 `created_at/created_by/updated_at/updated_by/deleted_at/deleted_by` 是服务端审计列，
  不进入写入 Input；`*_at` 使用数据库时钟，`*_by` 来自可信 Identity actor
- `where` 是客户端无法移除的固定条件，覆盖根查询、嵌套、统计与变更
- `read_only` 不进入写入 input；`insert_preset` / `update_preset` 由服务端强制写入
- 每个 Engine 必须配置一个显式 `PolicyProvider`，或由非空 `permissions` 自动生成；两者同时存在或同时缺失都拒绝启动
- `select` / `insert` / `update` 是字段白名单，`*` 表示全部，空列表表示拒绝；
  filter/sort/distinct/groupBy/having/search 同样受读取字段白名单约束
- `filter` 在 SQL 中限制可见/可改行；`check` 在发 SQL 前逐行验证新值，批量任一行失败则整次拒绝

明确的 mutation 契约与旧字段并存：

```graphql
updatePostByPk(id: ID!, input: PostUpdateInput!): Post
updatePosts(where: PostWhereInput!, input: PostUpdateInput!): PostMutationResult!
deletePostByPk(id: ID!): Post
deletePosts(where: PostWhereInput!): PostMutationResult!
restorePostByPk(id: ID!): Post
restorePosts(where: PostWhereInput!): PostMutationResult!
purgePostByPk(id: ID!): Post
purgePosts(where: PostWhereInput!): PostMutationResult!

type PostMutationResult {
  affectedCount: Int!
  returning: [Post!]!
}
```

不可数类名的批量字段使用 `Many` 后缀，避免与单条字段重名。

`purge` 会在同一条 PostgreSQL 语句中写入内部 `gql_audit_outbox`；缺表或写入失败会回滚
物理删除。迁移系统应执行 `pgsql.AuditOutboxDDL`，并让业务数据库角色只拥有 INSERT；
外部 sink 在提交后按 `event_key` 幂等消费。内部 outbox 表永不进入 GraphQL Schema。

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
结果指纹变化才推送（错误同样参与指纹，持续故障不会随每次变更重复推送）。
空闲零数据库负载，推送毫秒级。同构订阅（查询+变量+作用域相同）自动共享一份
重查流：热点订阅的重查 SQL 数与订阅者数解耦，后来者立即获最近结果补发；
慢消费者不阻塞扇出（未消费的旧结果被最新结果顶替）。`executor.Close()`
停止复制连接与重连循环（优雅关停用）。

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

订阅是 **latest-state** 语义，不是 durable event log：每次连接先发送当前查询结果，
CDC 只负责触发重查，慢消费者只保留最新结果。断线重连会重新读取当前状态，但不提供
历史事件 offset 或 exactly-once replay；需要审计事件流时应把 outbox/事件表作为显式
GraphQL 实体，或实现带持久 offset 契约的 `Endpoint.Subscribe`，不能把临时复制槽当事件存储。

## 数据库 schema

元数据按 `schema.schema`（缺省 `public`）加载，生成 SQL 中所有基表引用
带 schema 限定（`"public"."users"`），不依赖 search_path；变更 CTE 及其
读回引用为裸名（CTE 名与表名一致，语句作用域内命中）。

## schema 与操作文档加载

- `schema.file` 配置后把文件作为生成 catalog 的只读 artifact 加载；类型、参数、输入字段
  或坐标有任何漂移都会拒绝启动，不能用它声明引擎实际不支持的隐藏能力
- `builder.LoadDocuments(dir)` 在 Build 前加载目录下 `.graphql` 操作文档（持久化查询），
  操作按名注册并预热编译缓存；HTTP 请求省略 `query` 仅传 `operationName` 即可执行

## 生产安全开关

- `schema.introspection: false` 关闭自省（`__schema/__type` 拒绝，常规查询不受影响）
- `schema.persisted-only: true` HTTP 边界只接受持久化操作（原始查询文本 403），
  程序内 `Execute`/订阅 channel API 不受限（服务端代码可信）
- `Register`/`LoadDocuments` 仅限启动期：首次执行后注册表冻结，运行期调用明确报错
  （schema 重建与并发请求不互斥，冻结防静默数据竞争）
- 订阅 WebSocket 实现 graphql-transport-ws 完整状态机：未 `connection_init` 即
  subscribe 关 4401，init 超时 4408、重复 init 4429、重复订阅 ID 4409——
  鉴权握手不可绕过
- `builder.Observe(observer)` 仅在 Build 前安装。观测事件只有
  `execute/plan/authorize/database/handler`、耗时和稳定错误码，不包含 query、variables、
  token、cursor、tenant 或 user；observer panic 会被隔离，关闭时热路径约 2ns、零分配

## 性能要点

- 任意深度嵌套 = 单条 SQL，无 N+1
- 编译计划 LRU 缓存（默认 512），key 为操作名+查询文本，变量不参与 key；
  未命中经 singleflight 收敛，并发的同一冷查询只解析编译一次（防缓存击穿）
- 编译上下文走 `sync.Pool`，热路径零反射
- 整体 `input` 变量的变更依赖变量内容，自动跳过缓存（volatile）
- 响应直通：无 resolver 时 DB 返回的 `__root` 字节经 `MarshalJSON` 直接拼入响应，
  跳过「解包成 map 再序列化」往返
- `Execute` 直接返回标准 GraphQL JSON 字节；调用方确需对象树时自行按目标结构解码，
  不让所有请求承担通用 `map[string]any` 的分配成本
- resolver 宿主拍平单遍直收、无 `interface{}` 装箱中间层

### 性能基准

两类基准，运行 `go test -bench=. -benchmem`：

- **引擎微基准**（`gql/bench_test.go`，无需数据库）：固化上述两处优化为可回归基线。
  Apple M1 Max 实测（50 行列表响应）：

  | 基准 | ns/op | allocs/op |
  |------|-------|-----------|
  | `BenchmarkReplyBytes`（实际 `Execute` 字节出口） | ~1.6K | 1 |
  | `BenchmarkReplyUnpack`（解包重序列化） | ~165K | 3243 |
  | `BenchmarkReplySplit/旁路命中`（局部 Resolver） | ~19K | 41 |
  | `BenchmarkHosts`（64 宿主拍平） | ~0.6K | 11 |

  实际字节出口相对完整解包约 **100×**、分配从 3243 次降到 1 次；局部 Resolver
  也只解包命中分支，未命中分支保持原始 JSON 字节。

  codec 流式转换基准（`BenchmarkEncodeBytes`，100 行×2 个 ID 字段）：路径**无命中**时
  0 分配、~460MB/s 直接返回原字节（不启用 codec 的查询零开销）；命中时成本集中在
  sqids 编码本身，与 REST 通道单 id 编码成本一致。

- **端到端基准**（`gql/examples/bench_test.go`，需 demo 库在运行）：复用 demo 实际装配，
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
- 复合外键关系支持查询 JOIN（逐列 AND），不支持嵌套关系操作（connect/create，编译期明确报错）；
  复合自引用外键不生成递归字段；中间表识别限单列外键约束

设计细节见 [`../doc/gql-rework-plan.md`](../doc/gql-rework-plan.md) 与
[`../doc/pgsql-template-design.md`](../doc/pgsql-template-design.md)。升级与滚动发布见
[`MIGRATION.md`](MIGRATION.md)。
