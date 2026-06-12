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
k, _ := std.NewKonfig()                    // 配置（可声明虚拟字段/关系/排除表等）
meta, _ := gql.NewMetadata(k, db)          // db: *gorm.DB，自动加载表结构与外键关系
compile, _ := gql.NewCompiler(meta, []compiler.Dialect{pgsql.NewDialect()})
executor, _ := gql.NewExecutor(db, gql.NewRenderer(meta), meta, compile)

app.Post("/graphql", executor.Handler)     // fiber v3
```

## 查询能力

- 过滤：`where: { name: { eq/ne/gt/ge/lt/le/in/like/iLike/regex/iRegex/is/hasKey... } }`，
  支持 `and/or/not` 任意组合；`id: X` 是主键等值的快捷方式
- 排序：`sort: { name: ASC, age: DESC_NULLS_LAST }`（单对象或列表均可）
- 分页：`limit/offset` + `total`（窗口函数一次查询同时取数与总数）
- 关系：多对一/一对多/多对多（中间表）/递归（parent/children）自动生成，
  嵌套关系字段同样支持 `where/sort/limit/offset`
- 变更：`createX(input)` / `updateX(input, id|where)` / `deleteX(id|where)`，
  变更 CTE + 读回单条 SQL 原子完成；update/delete 强制要求条件
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

## 扩展新数据库（纯新增，零修改）

所有数据库相关能力都走自注册（`database/sql` 驱动同款模式），
将来支持 MySQL 只需**新增**以下代码，不修改任何现有文件：

1. **方言**：新建 `compiler/mysql` 包实现 `compiler.Dialect` 的 5 个方法
   （契约见接口注释：单行单列 __root JSON、参数槽位、MarkTable 等），
   `init` 中 `compiler.Register(NewDialect())`——空白导入即生效，
   `NewCompiler(meta, nil)` 自动按驱动名路由；已知驱动未注册方言会明确报错
2. **订阅唤醒源**：新增文件实现 `notifier` 接口（binlog 监听）并
   `registerNotifier("mysql", 工厂)`——执行器按 `db.Name()` 自动选取
3. **元数据**：`MysqlLoader` 已就绪，按驱动自动启用，无需任何动作

当前未注册 MySQL 的任何实现：连 MySQL 时编译器报「没有注册对应的SQL
方言实现」，订阅报「没有注册CDC唤醒源」，不会静默出错。

## 当前限制

- MySQL 方言未实现（扩展方式见上节；已知驱动未注册方言会明确报错，不会静默回退）
- 游标分页（`first/last/after/before/pageInfo`）编译期明确报错，未实现
- 嵌套写入（`connect/disconnect`、upsert）未实现
- 同一 mutation 内不能两次变更同一张表（变更 CTE 同名限制）
- 统计查询（`xxxStats`）schema 已生成，编译未实现

设计细节见 [`../doc/gql-rework-plan.md`](../doc/gql-rework-plan.md) 与
[`../doc/pgsql-template-design.md`](../doc/pgsql-template-design.md)。
