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

## 当前限制

- MySQL 方言仅有接口占位，未实现
- 游标分页（`first/last/after/before/pageInfo`）编译期明确报错，未实现
- 嵌套写入（`connect/disconnect`、upsert）未实现
- 同一 mutation 内不能两次变更同一张表（变更 CTE 同名限制）
- 统计查询（`xxxStats`）schema 已生成，编译未实现

设计细节见 [`../doc/gql-rework-plan.md`](../doc/gql-rework-plan.md) 与
[`../doc/pgsql-template-design.md`](../doc/pgsql-template-design.md)。
