# gql 引擎完整演示

博客模型（用户/文章/标签/评论）演示引擎全部能力。**本文档所有请求均经过实测**，
可直接复制执行。

## 快速开始

```bash
cd gql/example
docker compose up -d        # PostgreSQL 16 + 建表种子数据（唯一要求 wal_level=logical）
go run .                    # GraphQL服务: http://localhost:8080/graphql
```

按住下文示例逐个 curl 即可。schema 自动生成于 `./cfg/schema.graphql`，
可直接喂给 GraphiQL/Apollo 等工具（自省也完整支持，工具直连即可）。

## 目录

1. [查询](#查询) — 关系嵌套/过滤/排序/分页/游标/去重/jsonb/递归/搜索/统计
2. [变更](#变更) — 增删改/批量/upsert/嵌套写入
3. [自定义 Resolver](#自定义-resolver)
4. [持久化查询](#持久化查询)
5. [订阅](#订阅cdc)
6. [配置参考](#配置参考)

## 查询

```bash
GQL() { curl -s http://localhost:8080/graphql -H 'Content-Type: application/json' -d "$1"; }
```

**关系嵌套 + 全文搜索**（任意深度单条 SQL，零 N+1；中文搜索开箱即用）：

```bash
GQL '{"query":"{ posts(search: \"数据库\") { items { title author: user { name } tags { name } } total } }"}'
# {"data":{"posts":{"items":[{"author":{"name":"张三"},"tags":[...],"title":"PostgreSQL数据库引擎选型"},...],"total":2}}}
```

**过滤组合**（and/or/not、同字段多操作符）与 **jsonb 包含**：

```bash
GQL '{"query":"{ users(where: { profile: { contains: { vip: true } } }) { items { name } } }"}'
GQL '{"query":"{ users(where: { profile: { hasKey: \"city\" } }) { items { name } } }"}'
```

**limit/offset + total**（窗口函数一次查询同时取数与总数）：

```bash
GQL '{"query":"{ posts(limit: 2, offset: 1, sort: { id: ASC }) { items { id title } total } }"}'
```

**游标分页**（keyset，深翻页性能恒定；首页变量传 null 即可）：

```bash
GQL '{"query":"query ($c: Cursor) { posts(first: 2, after: $c, sort: { id: ASC }) { items { id } pageInfo { hasNext hasPrev end } } }","variables":{"c":null}}'
# 用返回的 pageInfo.end 作为下一页的 $c；hasNext=false 即到底
```

**去重 / 统计聚合 / 递归全树**：

```bash
GQL '{"query":"{ posts(distinct: [\"userId\"]) { items { userId } } }"}'
GQL '{"query":"{ postStats(groupBy: [\"userId\"]) { key count } }"}'
GQL '{"query":"{ postStats(groupBy: [\"userId\"], having: { count: { gt: 1 } }) { key count } }"}'   # 分组后按聚合值过滤(HAVING)
GQL '{"query":"{ comments(id: 1) { items { content descendants(depth: 5) { content } } } }"}'   # 全部后代
GQL '{"query":"{ comments(id: 3) { items { ancestors { content } } } }"}'                        # 祖先链
```

**fragment 与 __typename**（Apollo 客户端兼容）：

```bash
GQL '{"query":"fragment F on User { id name } { __typename users(id: 1) { __typename items { ...F } } }"}'
```

## 变更

**创建/更新/删除**（update/delete 强制要求 id 或 where，杜绝全表误操作）：

```bash
GQL '{"query":"mutation { createUser(input: { name: \"赵六\", email: \"zhao@demo.dev\" }) { id name } }"}'
GQL '{"query":"mutation { updateUser(input: { name: \"赵六改\" }, where: { email: { eq: \"zhao@demo.dev\" } }) { name } }"}'
GQL '{"query":"mutation { deleteUser(where: { email: { eq: \"zhao@demo.dev\" } }) }"}'   # 返回删除行数
```

**批量插入与 upsert**（复数字段；N 行 = 1 条 SQL）：

```bash
GQL '{"query":"mutation { upsertUsers(input: [{ name: \"张三改\", email: \"zhang@demo.dev\" }, { name: \"钱七\", email: \"qian@demo.dev\" }], on: [\"email\"]) { name } }"}'
# email冲突的行被更新，新email插入；on缺省按主键冲突
```

**嵌套写入**（connect 挂接既有行 / disconnect 解除 / create 内联建新行，同语句原子）：

```bash
GQL '{"query":"mutation { createUser(input: { name: \"孙八\", email: \"sun@demo.dev\", posts: { create: [{ title: \"内联新文章\" }] } }) { id } }"}'
GQL '{"query":"mutation { updatePost(input: { tags: { connect: [2], disconnect: [1] } }, id: 1) { id } }"}'
```

注意（PostgreSQL 快照语义）：嵌套写入原子生效，但**同一请求的读回看不到关系变更**，
确认结果请再发一个查询。

## 自定义 Resolver

SQL 表达不了的字段逻辑用 Resolver。三步接入（完整代码见 `main.go`）：

**1. 配置声明虚拟字段**（无列，由 resolver 在执行后填充）：

```go
k.Set("metadata.classes", map[string]*gql.ClassConfig{
    "User": {
        Table: "users",
        Fields: map[string]*gql.FieldConfig{
            "sign": {Type: "String", IsNullable: true, Resolver: "sign"},
        },
    },
})
// 等价 yaml：metadata.classes.User.fields.sign: {type: String, nullable: true, resolver: sign}
```

**2. 实现接口并注册**：

```go
type sign struct{}

func (sign) Name() string { return "sign" } // 与配置里的 resolver 名对应

// source 是该行已查出的字段——resolver 只能读到查询选择了的字段，
// 依赖 email 时查询需一并选择 email
func (sign) Resolve(_ context.Context, source map[string]any, _ map[string]any) (any, error) {
    if email, ok := source["email"]; ok {
        return fmt.Sprintf("%v <%v>", source["name"], email), nil
    }
    return fmt.Sprint(source["name"]), nil
}

// 可选：实现 ResolveBatch 后列表整批一次调用（如批量签名URL、调外部API），免N+1
func (sign) ResolveBatch(ctx context.Context, sources []map[string]any, args map[string]any) ([]any, error) { ... }

executor.Register(sign{})
```

> 列表场景下 `Resolve` 会被并发调用，实现须线程安全；有状态逻辑请实现 `BatchResolver`。

**3. 像普通字段一样查询**：

```bash
GQL '{"query":"{ users(id: 1) { items { name email sign } } }"}'
# {"data":{"users":{"items":[{"email":"zhang@demo.dev","name":"张三","sign":"张三 <zhang@demo.dev>"}]}}}
```

要点：虚拟字段不进 SQL、不进 CreateInput/UpdateInput；未注册的 resolver 名执行期明确报错；
嵌套列表上的 BatchResolver 对整个列表只调用一次（引擎 e2e 有调用次数断言）。

## 持久化查询

`queries/*.graphql` 中的命名操作启动时注册并预热编译缓存，请求省略 query 仅传 operationName：

```bash
GQL '{"operationName":"PostList","variables":{"kw":"数据库"}}'
GQL '{"operationName":"AddTag","variables":{"name":"新标签"}}'
```

## 订阅（CDC）

基于 WAL 逻辑复制：表变更毫秒级推送、空闲零数据库负载。GET /graphql 为
[graphql-transport-ws](https://github.com/enisdenjo/graphql-ws/blob/master/PROTOCOL.md)
升级入口，Apollo/graphql-ws 客户端直连。协议帧（已用 ws 客户端实测）：

```jsonc
→ {"type":"connection_init"}
← {"type":"connection_ack"}
→ {"id":"1","type":"subscribe","payload":{"query":"subscription { users { total } }"}}
← {"id":"1","type":"next","payload":{"data":{"users":{"total":5}}}}   // 首推当前结果
// 任何途径写入users表后（毫秒级）：
← {"id":"1","type":"next","payload":{"data":{"users":{"total":6}}}}
```

## 配置参考

| 键 | 说明 | demo取值 |
|----|------|----------|
| `metadata.classes.X.search` | 实体搜索列（声明后获得 search 参数） | `posts: [title, content]` |
| `metadata.classes.X.fields.f.resolver` | 字段绑定自定义 resolver | `sign` |
| `search.mode` / `search.config` | 搜索模式覆盖（缺省自动探测：jieba/zhparser→tsvector，否则 pg_trgm） | 自动 |
| `schema.file` | 生产从文件加载 schema | 未设（renderer 生成） |
| `subscription.publication` / `.dsn` | CDC 发布名 / 复制连接 | 缺省 |

性能相关索引（`schema.sql` 已建好示范）：搜索列 `gin_trgm_ops`、jsonb 列 GIN、
递归 parent_id、外键列。
