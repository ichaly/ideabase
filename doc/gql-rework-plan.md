# GQL 模块重构方案

> 分支 `feature/gql-rework`。目标：数据库自动映射标准 GraphQL（对标 graphjin），
> 单条 SQL 消除 N+1，支持自定义 Resolver，性能优先。

## 现状评估

| 组件 | 状态 | 处置 |
|------|------|------|
| protocol / metadata（四种 Loader、关系推导、双索引） | 完整 | 保留 |
| renderer（schema 生成：Result/WhereInput/SortInput/Stats） | 完整 | 保留，关系字段补充 where/sort/limit 参数 |
| compiler/pgsql 的 where/sort 子句构建 | 可用但 `X`/`XWithAlias` 双轨冗余 | 合并为带 alias 参数的单轨 |
| compiler/pgsql/select | **嵌套关系无关联条件**（未使用 Relation 元数据），别名计数递归冲突 | 重写 |
| compiler/pgsql 的 insert/remove | TODO 空壳；mutation 路由按 `insert/update/delete` 字段名分发，与 renderer 生成的 `createX/updateX/deleteX` 不匹配 | 重写 |
| executor | `__root` JSON 未解包、错误地以 operationName 包装 data、无编译缓存 | 重写执行链路 |
| resolver | 仅包装 Executor，无注册/分发机制 | 重新设计 |
| compiler/mysql | 空壳 | 暂保留接口占位，文档注明未实现 |

## 架构（数据流）

```
GraphQL 请求
  → gqlparser 解析/校验（schema 由 renderer 从 metadata 生成，或从文件加载）
  → 编译缓存命中? ──是──→ 取 plan
        │否
  → Dialect(策略模式).Build → plan{sql, 参数槽位}
  → 填充变量 → 单条 SQL 执行（LATERAL JOIN + JSONB 聚合，零 N+1）
  → __root JSON 解包为 data（顶层 key = 字段别名）
  → Resolver 后处理（自定义字段，批量接口避免 N+1）
  → 响应
```

## 关键设计

### 1. SELECT 编译（遵循 doc/pgsql-template-design.md 模板）

- 关联条件由 `protocol.Relation` 元数据驱动：
  - `MANY_TO_ONE`：`WHERE 目标.主键 = 父别名.外键` 返回单对象
  - `ONE_TO_MANY`：`WHERE 目标.外键 = 父别名.主键` + `JSONB_AGG`
  - `MANY_TO_MANY`：`INNER JOIN 中间表 ON 中间表.目标键=目标.主键 WHERE 中间表.源键=父别名.主键`
  - `RECURSIVE`：children/parents 一层外键关联；`level` 深度查询用递归 CTE（二期）
- 别名计数器挂在编译 Context 上全局递增，杜绝递归重置冲突
- 根字段输出 `items`/`total`（对应 `XxxResult` 契约）；嵌套关系输出纯数组/对象
- 嵌套关系字段同样支持 where/sort/limit/offset（renderer 同步补充参数定义）

### 2. 编译缓存（性能核心）

`Build` 不再直接闭合参数值，产出可复用执行计划：

```go
type Plan struct {
    SQL   string
    Slots []Slot // 字面量值 或 变量名，执行时按序生成 args
}
```

- 缓存 key：`hash(query) + operationName`；变量值不参与 key（只是参数）
- LRU 上限可配；命中路径零解析、零编译
- Context 继续走 sync.Pool

### 3. Resolver 系统

```go
type Resolver interface {
    Name() string
    Resolve(ctx context.Context, source map[string]any, args map[string]any) (any, error)
}
// 可选实现：列表场景一次调用，避免 N+1
type BatchResolver interface {
    Resolver
    ResolveBatch(ctx context.Context, sources []map[string]any, args map[string]any) ([]any, error)
}
```

- 注册：`Executor` 选项 `WithResolver(r ...Resolver)`；metadata 中 `Field.Resolver` 按名绑定
- 编译期：resolver 虚拟字段不进 SQL，但自动带出其依赖列（如主键）
- 执行期：按结果树路径分发；列表节点优先走 `ResolveBatch`

### 4. schema / 文档加载

- schema 统一入口：优先 `cfg/schema.graphql` 文件（生产），否则 renderer 现场生成（开发）
- 支持加载 `.graphql` 操作文档（持久化查询），配合编译缓存预热

## 阶段计划（全部完成 ✅）

1. **P1 测试基线** ✅：修路径解析（`utl.Root()`=cwd 语义变化的连锁失败）、过时断言、空格归一化；附带修复 constant.go 切片别名覆写 bug
2. **P2 SELECT 重写** ✅：关系关联条件（元数据驱动）+ 全局别名 + 嵌套参数 + 单轨 where/sort
3. **P3 Mutation** ✅：`createX/updateX/deleteX` 路由 + 变更 CTE + 统一读回；update/delete 强制条件
4. **P4 执行链路** ✅：`__root` 解包、规范 data 组织、Plan LRU 缓存、常量下沉 protocol 解除 import cycle
5. **P5 Resolver** ✅：注册/分发/批量（BatchResolver 免 N+1）、编译期绑定收集进 Plan
6. **P6 加载与文档** ✅：schema.file 配置、LoadDocuments 持久化查询、列表关系字段嵌套参数、README
7. **P7 订阅** ✅：轮询推送（指纹比对）+ graphql-transport-ws WebSocket 传输
8. **P8 订阅改纯 CDC** ✅：WAL 逻辑复制（pgoutput + 临时槽）表级变更唤醒，移除轮询；
   Plan 记录涉及表集合；部署仅需 `wal_level=logical`

## 遗留事项（后续版本）

- MySQL 方言实现（接口已就位，参照 pgsql 单元化结构）
- 游标分页（first/last/after/before/pageInfo，编译期明确报错）
- 统计查询 `xxxStats` 编译（schema 已生成）
- 嵌套写入（connect/disconnect/upsert）
- `metadata.go` 中 loader_base 反向关系挂在主键字段会被多个外键覆写（仅影响极端多外键场景）
