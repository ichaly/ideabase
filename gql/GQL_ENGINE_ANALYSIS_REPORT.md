# GQL 引擎架构审查与完善方案

> 审查范围：`/Users/Chaly/Documents/Workspace/ideabase/gql`
>
> 审查基线：`2e63dac`
>
> 报告日期：2026-07-21
>
> 审查方式：源码、测试、基准与业内官方资料对照；本报告不包含代码改动

## 一、执行摘要

这套引擎已经不是原型，而是一个完成度较高的 PostgreSQL GraphQL-to-SQL 编译器：

- 查询编译、嵌套关系、分页、聚合、全文检索、计划缓存和 JSON 字节直通做得很好。
- Resolver、Batch Resolver、Remote Join、Codec、CDC 订阅具备明确的扩展模型。
- 查询性能路径有鲜明优势，部分实现已经达到甚至优于常见通用 GraphQL Resolver 引擎。

但如果目标是：

> 建完数据库表后，基本不再写业务代码，只通过标准 GraphQL 完成真实业务。

目前仍有明显差距。核心缺口不是更多 CRUD 操作，而是：

1. 缺少统一的 mutation 事务执行模型。
2. 缺少角色、行、列、操作级权限和写入检查。
3. 缺少声明式业务规则、数据库函数和多表 Action。
4. 部分 GraphQL 标准执行语义尚未完整实现。
5. 数据库元数据读取深度不足，无法真正做到“表即 API”。
6. 嵌套写入目前只覆盖有限的一层关系操作。

综合判断：

| 维度 | 当前成熟度 |
|---|---:|
| 查询编译与 SQL 性能 | 8.5/10 |
| CRUD/关系突变 | 6/10 |
| GraphQL 标准兼容 | 5.5/10 |
| 权限和安全治理 | 3.5/10 |
| 低代码业务表达 | 4/10 |
| 扩展机制 | 6.5/10 |
| 生产可观测与运维 | 5/10 |

作为“高性能自动 CRUD 引擎”，完成度大约 75%；作为“几乎零代码的业务后端平台”，大约 40%～50%。

## 二、已经做得很好的部分

### 2.1 查询单 SQL 路线正确

嵌套关系通过 LATERAL JOIN 和 JSONB 聚合生成单条 SQL，避免传统 Resolver 模型的 N+1。这是整个项目最有价值的基础。

选择集驱动列投影、统计按需计算、关系嵌套、游标分页、默认 limit、最大深度保护都已经进入编译阶段，而不是运行期拼装。

主要证据：

- [`compiler/pgsql/select.go`](compiler/pgsql/select.go)
- [`compiler/pgsql/stats.go`](compiler/pgsql/stats.go)
- [`compiler/pgsql/cursor.go`](compiler/pgsql/cursor.go)
- [`compiler/pgsql/search.go`](compiler/pgsql/search.go)

### 2.2 热路径性能真实有效

本机 Apple M1 Max 实测：

| 基准 | 结果 |
|---|---:|
| 字节直通响应 | 约 1.55µs，1 alloc |
| 完整 map 解包重编码 | 约 156µs，3243 allocs |
| 局部 Resolver 解包 | 约 18.6µs，41 allocs |
| 64 个 Resolver 宿主拍平 | 约 638ns |
| Codec 有命中扫描 | 约 320MB/s，1 alloc |
| Codec 无命中扫描 | 约 459MB/s，0 alloc |

这说明字节直通、路径局部解包、计划缓存、参数槽位这些设计都值得保留，不应因后续业务能力建设而推倒重来。

相关实现：

- [`executor.go`](executor.go)
- [`encode.go`](encode.go)
- [`cache.go`](cache.go)
- [`compiler/context.go`](compiler/context.go)

### 2.3 扩展机制方向合理

现有扩展层次包括：

- 字段 Resolver
- Batch Resolver
- 根 Query/Mutation Resolver
- Resolver Middleware
- Remote Join
- Scalar Codec

特别是 Batch Resolver 跨整个结果树一次收集宿主，而不是每个父节点一批，这个设计很好。

相关实现：

- [`resolver.go`](resolver.go)
- [`remote.go`](remote.go)
- [`codec.go`](codec.go)

### 2.4 已有安全护栏有效但不完整

已有：

- update/delete 强制条件
- 行级 scope 注入
- persisted-only
- 关闭 introspection
- 最大深度
- 默认分页限制
- Resolver panic 隔离
- 注册表启动后冻结

这些属于可靠的工程基础，但还不是完整授权系统。

## 三、关键实现问题

### 3.1 P0：mutation 执行语义需要先纠正

默认多根 mutation 被编译进同一个 `WITH`：

- [`compiler/pgsql/mutation.go#L47`](compiler/pgsql/mutation.go#L47)
- [`compiler/pgsql/mutation.go#L84`](compiler/pgsql/mutation.go#L84)

PostgreSQL 官方明确说明：data-modifying CTE 之间并发执行、顺序不可预测、共享同一快照，不能看到彼此对表的修改。

而 GraphQL 规范要求顶层 mutation 字段按文本顺序串行完成。

因此当前这样的操作：

```graphql
mutation {
  updateAccount(...)
  createOrder(...)
}
```

虽然单条 SQL 整体失败会回滚，但不能保证第二项看见第一项的数据库变化，也不能依赖触发器或副作用执行顺序。这不等价于 GraphQL 串行 mutation。

官方依据：

- [GraphQL Mutation execution](https://spec.graphql.org/October2021/#sec-Mutation)
- [PostgreSQL data-modifying CTE](https://www.postgresql.org/docs/current/queries-with.html)

### 3.2 P0：混合 Resolver 后失去统一事务

只要操作中出现根 Resolver，整份操作进入 `executeRootResolvers`：

- [`resolver.go#L413`](resolver.go#L413)

默认数据库字段随后被拆成单字段 SQL 独立执行：

- [`resolver.go#L455`](resolver.go#L455)

这会导致：

- 自定义 Resolver 和默认 CRUD 不共享事务。
- 中间字段失败时，前面已经提交的写入无法回滚。
- Resolver 通常捕获外部 `*gorm.DB`，无法自动使用引擎事务。
- “突变前检查 + 多表写入”只能手工写完整 Resolver。

当前执行器直接通过固定的 `my.database` 执行计划，也没有事务感知的执行上下文：

- [`executor.go#L433`](executor.go#L433)

这是当前最需要解决的架构断层。

### 3.3 P1：嵌套写入只真正支持一层

关系 `create` 会递归解析目标 `CreateInput`，目标行自身也可能包含关系操作：

- [`compiler/pgsql/mutation.go#L601`](compiler/pgsql/mutation.go#L601)

但后面创建子行时只调用 `writeInsertValues`，没有继续处理子行的 `row.ops`：

- [`compiler/pgsql/mutation.go#L779`](compiler/pgsql/mutation.go#L779)
- [`compiler/pgsql/mutation.go#L829`](compiler/pgsql/mutation.go#L829)

因此二层嵌套关系可能通过 GraphQL Schema 校验并被解析，但更深一层关系操作没有真正执行。这里至少应该先明确拒绝，不能静默忽略；最终应改造成关系写入图编译器。

当前还缺：

- 多对一嵌套创建
- 嵌套 update/upsert/delete
- connect-or-create
- replace/set 关系集合
- 复合外键关系写入
- 批量父子写入关联
- 任意深度写入
- 嵌套节点独立 on-conflict

### 3.4 P1：按 where 更新多行，却只返回第一行

`updateX(where: ...)` 可以影响多行，但 Schema 返回单对象。读回形态是 `shapeSingle`，内部固定 `LIMIT 1`：

- [`compiler/pgsql/mutation.go#L348`](compiler/pgsql/mutation.go#L348)
- [`compiler/pgsql/select.go#L604`](compiler/pgsql/select.go#L604)

结果是：

- 实际更新 N 行。
- 客户端只看到任意一行。
- 没有 `affectedCount`。
- 零行命中可能返回 null，但 Schema 声明为非空。

建议拆成明确的两类契约：

```graphql
updateUserByPk(...)
updateUsers(where: ..., input: ...): UserMutationResult!
```

其中结果至少包含：

```graphql
type UserMutationResult {
  affectedCount: Int!
  returning: [User!]!
}
```

### 3.5 P1：缺少真正的权限系统

当前 `scope` 只是固定的：

```text
column = context value
```

配置模型中不存在：

- role
- 每操作权限
- 列级 select/insert/update 权限
- 行级布尔表达式
- insert/update check
- 服务端字段 preset
- 聚合权限
- 关系穿透权限
- 字段脱敏策略
- 匿名角色与默认拒绝

配置定义可见：

- [`internal/config.go#L32`](internal/config.go#L32)
- [`internal/config.go#L56`](internal/config.go#L56)

这与 Hasura、PostGraphile/RLS、GraphJin 的主要差距就在这里。真正自动暴露数据库时，权限层比 CRUD 功能更重要。

### 3.6 P1：数据库元数据读取不够完整

PostgreSQL Loader 当前主要读取：

- 表
- 列名和类型
- nullable
- 主键
- 外键

实现位置：

- [`metadata/loader_pgsql.go#L20`](metadata/loader_pgsql.go#L20)

尚未读取：

- column default
- identity/generated column
- unique constraint/index
- check constraint
- enum/domain
- 数组元素类型
- view/materialized view
- function/procedure
- index 与搜索能力
- delete/update cascade
- column privileges

直接影响零代码体验：

- 非空但有数据库默认值的字段仍可能被错误渲染成必填。
- generated column 可能被暴露为可写。
- upsert 的 `on` 使用字符串，不能根据真实唯一约束生成类型安全枚举。
- PostgreSQL enum/domain 不能自然成为 GraphQL 类型。
- 无法自动暴露数据库函数作为业务 mutation。

### 3.7 P1：GraphQL 标准执行仍有缺口

最明显的是 `@skip`/`@include`：Schema 和 introspection 声明了它们，但编译执行路径没有处理字段指令。

此外还需要系统验证和完善：

- 变量运行期 coercion 和必填变量检查
- 非空字段错误冒泡
- `errors.path`、`locations`、`extensions`
- 字段错误部分成功语义
- 字段合并和重复 response key
- mutation 顶层串行
- subscription 根 `__typename`
- 自定义标量错误的标准化

当前数据库错误多数只是 `gqlerror.Wrap`，缺乏稳定错误码、字段路径和约束信息。

### 3.8 P2：查询成本保护仍不足

当前主要保护是：

- 最大选择集深度
- 默认列表 limit
- persisted-only

实现位置：

- [`compiler.go#L97`](compiler.go#L97)
- [`compiler/pgsql/select.go#L636`](compiler/pgsql/select.go#L636)

但深度并不等于成本。仍需考虑：

- 同层大量 alias
- 多个顶层统计字段
- 多个高成本远程字段
- 大型 groupBy/having
- 大量 batch mutation 参数
- 递归关系与普通嵌套组合
- 返回 JSON 字节数
- SQL planner 复杂度

需要结构化 cost model，而不只是 max-depth。

### 3.9 P2：错误、并发和可靠性能力不完整

建议补充：

- PostgreSQL SQLSTATE 到稳定 GraphQL error code 的映射
- unique/FK/check/not-null 的结构化错误
- statement timeout
- deadlock/serialization retry 策略
- mutation idempotency key
- optimistic concurrency/version check
- expected affected rows
- after-commit outbox
- 审计日志

外部服务调用不能和数据库共享 ACID 事务，必须明确采用 Outbox 或 Saga，而不是把 HTTP 调用放进数据库事务中假装原子。

## 四、与业内方案的差距

| 能力 | 当前 GQL | Hasura | PostGraphile | GraphJin |
|---|---|---|---|---|
| 自动表到 GraphQL | 强 | 强 | 强 | 强 |
| 单 SQL 嵌套查询 | 强 | 强 | 依实现/插件 | 强 |
| 字节响应快路径 | 很强 | 强 | 一般 | 强 |
| 角色/行/列权限 | 弱 | 很强 | 借助 PG RLS 很强 | 强 |
| 嵌套写入 | 基础一层 | 成熟 | 较成熟 | 成熟 |
| DB 函数自动暴露 | 无 | 支持 | 核心能力 | 支持 |
| 自定义业务 Action | Go Resolver | Actions | Plugins/Functions | Workflows/Resolvers |
| 可靠事件/Webhook | CDC 订阅 | Event Triggers | 插件/DB | Workflows/Watches |
| 事务化多表业务 | 不统一 | 较强 | DB 函数/事务 | 较强 |
| 标准 GraphQL 完整度 | 中 | 高 | 高 | 高 |
| 多数据库执行 | 实际仅 PG | 多源 | PG 专注 | 多源 |
| 运维、Explain、指标 | 较弱 | 成熟 | 成熟 | 成熟 |

### 4.1 Hasura

最值得借鉴的是：

- 基于角色的操作级权限
- 行级布尔表达式
- 列级权限
- insert/update check
- session variable preset
- nested insert/upsert
- Actions
- Event Triggers

参考：

- [Hasura Permission Rules](https://hasura.io/docs/2.0/auth/authorization/permissions/)
- [Hasura PostgreSQL Insert Mutations](https://hasura.io/docs/2.0/mutations/postgres/insert/)
- [Hasura Actions](https://hasura.io/docs/2.0/actions/overview/)
- [Hasura Event Triggers](https://hasura.io/docs/2.0/event-triggers/overview/)

不建议机械复制其全部 API，最需要吸收的是权限元数据和写入检查模型。

### 4.2 PostGraphile

最值得借鉴的是“把 PostgreSQL 本身作为业务扩展语言”：

- PostgreSQL RLS
- 数据库函数
- 复合类型
- 注释/Smart Tags
- 插件化 schema 扩展

这一路线特别适合“少写 Go、性能高、多表事务强”的目标。

参考：

- [PostGraphile Security](https://postgraphile.org/postgraphile/5/security)
- [PostGraphile Smart Tags](https://postgraphile.org/postgraphile/5/smart-tags)

### 4.3 GraphJin

GraphJin 最接近当前项目的编译器路线，值得参考：

- GraphQL-to-SQL 编译
- 角色和行级安全
- 查询 allow-list
- 多数据源治理
- 工作流和扩展动作
- 缓存及失效
- 审计与 agent/API 双入口治理

参考：

- [GraphJin](https://github.com/dosco/graphjin)

### 4.4 Prisma、PostgREST/Supabase

Prisma 更像代码生成数据层，不是直接的零代码 GraphQL 引擎竞品。

PostgREST/Supabase 值得借鉴 PostgreSQL RLS、RPC 和数据库优先的安全模型，但其查询表达形态与标准 GraphQL 不同。

## 五、建议的目标架构

不要继续让“默认 SQL CRUD”和“Resolver”成为两条互斥执行通道。建议统一成 Operation Plan：

```text
Parse / Validate / Variable Coercion / Directive
                    ↓
      Auth + Policy + Cost Analysis
                    ↓
              OperationPlan
              /           \
       QueryPlan         MutationPlan
     单 SQL 快路径       有序 Step 列表
                            ↓
                   单数据库事务边界
                            ↓
        CRUD / DB Function / Action / Resolver
                            ↓
              Readback + Error Mapping
                            ↓
                    After Commit
```

### 5.1 QueryPlan

查询继续保留现有设计：

- 单 SQL
- LATERAL JOIN
- 选择集驱动投影
- 计划缓存
- 参数槽位
- JSON 字节直通
- Resolver 命中分支局部解包

这条链路只需要补权限、指令、成本和错误语义，不需要重写。

### 5.2 MutationPlan

每个顶层 mutation 字段编译为一个有序 Step：

```text
Begin transaction
  Step 1: before/check/preset
  Step 1: execute
  Step 1: readback

  Step 2: before/check/preset
  Step 2: execute
  Step 2: readback
Commit
AfterCommit / Outbox dispatch
```

默认同一数据源建议使用整份 mutation operation 事务。远程服务无法纳入数据库 ACID，应明确走：

- Outbox
- Saga/补偿
- AfterCommit 事件

不要为了“一条 SQL”牺牲 mutation 正确性。查询继续坚持单 SQL；mutation 应优先“单事务、正确顺序、少往返”。

可以使用 pgx Batch 减少往返，但必须保持服务端串行执行。只有能证明完全等价的纯 SQL 步骤才允许融合优化。

### 5.3 统一处理器抽象

默认 CRUD 也应成为内部 Handler，而不是编译器的特殊旁路。建议统一为：

```text
FieldHandler
  - Compile/Prepare
  - Execute
  - Complete

Handler 实现：
  - SQLQueryHandler
  - SQLMutationHandler
  - DBFunctionHandler
  - DeclarativeActionHandler
  - ResolverHandler
  - RemoteHandler
```

这样 `Wrap`、事务、权限、追踪和错误处理可以一致覆盖默认 CRUD 与自定义字段，同时保留 SQL Handler 的编译快路径。

## 六、突变前逻辑的优雅方案

不建议只增加一个粗粒度 `BeforeMutation(func)`。长期会变成不可组合、不可缓存、事务语义模糊的万能钩子。

建议分四层。

### 6.1 声明式 Policy：优先使用，零业务 Go 代码

支持：

- allow/deny
- row filter
- column allowlist
- preset
- check
- max affected rows
- required predicate
- role/session 表达式

这些规则应尽量编译进 SQL，性能最高。

策略结构可以分为：

```text
RolePolicy
  SelectPolicy
    - columns
    - filter
    - allowAggregations
    - limit
  InsertPolicy
    - columns
    - presets
    - check
  UpdatePolicy
    - columns
    - filter
    - presets
    - check
  DeletePolicy
    - filter
```

如果不同角色会改变 Schema 可见字段或 SQL 结构，计划缓存键需要加入 `role + policyVersion`；scope/session 的具体值仍保持参数化，不进入缓存键。

### 6.2 声明式 Rule/Validator

适合：

- 金额必须大于零
- 状态只能按状态机迁移
- 库存必须充足
- 某字段组合互斥
- 更新前必须存在关联记录
- 影响行数不能超过阈值

简单规则编译为 SQL predicate；需要读取数据库的校验应在同一事务中执行。

### 6.3 数据库函数/存储过程自动暴露

这是满足“少代码、高性能、多表原子操作”的最佳扩展方式。

例如数据库中定义：

```sql
create function place_order(...)
returns orders
```

引擎自动读取参数、返回类型和注释，生成：

```graphql
mutation {
  placeOrder(input: ...) {
    id
    status
  }
}
```

函数内部可以：

- 写订单
- 扣库存
- 写流水
- 做锁和约束检查
- 一次事务完成

无需写 Go Resolver，性能和事务语义都很好。配合注释或 allowlist 控制哪些函数可以暴露。

建议按函数属性映射能力：

- `STABLE/IMMUTABLE`：Query 字段
- `VOLATILE` 且显式 allowlist：Mutation 字段
- 返回实体/复合类型：自动生成可选择结果
- 返回标量：直接输出
- 返回集合：支持过滤、排序或明确只读回集合

### 6.4 Typed Resolver：最后逃生口

保留现有 Resolver，但应改为接收统一执行上下文：

```text
MutationContext
  - Tx
  - Identity/Role/Session
  - Coordinate
  - NormalizedInput
  - SelectionSet
  - OperationID
```

并提供明确生命周期：

- `Before`
- `Around/Execute`
- `After`：提交前
- `AfterCommit`
- `OnRollback`

Resolver 不应捕获全局 `*gorm.DB`；否则无法自动进入引擎事务。

### 6.5 生命周期边界建议

| 生命周期 | 是否在事务内 | 适合逻辑 |
|---|---:|---|
| Before | 是 | 权限、输入归一化、数据库状态校验 |
| Execute | 是 | 主写入、多表原子操作 |
| After | 是 | 审计表、派生表、结果转换 |
| AfterCommit | 否 | 消息、Webhook、邮件、外部服务 |
| OnRollback | 否 | 日志、指标、补偿调度 |

外部网络调用不应放在长事务中；可靠投递应通过事务内 Outbox 写入，提交后消费。

## 七、多表业务的推荐表达层次

建议形成三级能力。

### 7.1 Level 0：自动 CRUD

建表后直接得到：

- 查询
- 单条/批量创建
- 更新/删除
- upsert
- 常见关系 connect/create/disconnect

不写任何配置和代码。

### 7.2 Level 1：元数据声明

少量 YAML/JSON 完成：

- 权限
- scope
- preset
- validation/check
- 字段隐藏
- relation override
- mutation 行为限制

仍不写 Go。

### 7.3 Level 2：数据库 Action 或声明式 Workflow

复杂多表业务通过：

- PostgreSQL function/procedure
- 引擎声明式事务 Action

生成一个标准 GraphQL Mutation 字段。

标准 GraphQL 本身不能表达“把第一个 mutation 返回的 ID 传给第二个字段”这种流程。因此“完全只靠任意标准 GraphQL 组合实现所有业务”在信息表达上不可能。必须把业务信息放在以下至少一个地方：

- 数据库约束/函数
- 声明式元数据
- Resolver 代码

最佳目标应是“业务不写 Go”，而不是“业务规则无处声明”。

## 八、嵌套写入的推荐实现

### 8.1 从 relationOp 列表升级为 MutationGraph

建议把输入编译成写入图：

```text
MutationGraph
  Node: insert/update/delete/upsert/connect/disconnect
  Edge: depends-on / provides-key / relation-bond
```

编译阶段完成：

1. 遍历整个嵌套 input。
2. 为每个写入节点分配唯一别名。
3. 根据主外键和输入引用建立依赖边。
4. 检查循环依赖和复合键完整性。
5. 拓扑排序。
6. 生成有序 MutationStep 或安全的依赖 CTE。
7. 在事务中执行写入并完成最终 readback。

### 8.2 批量父子关联

批量父行和子行不能继续依赖单行标量子查询。可以为输入行加入内部 ordinal：

```text
__input_index
```

通过 `VALUES`/临时 CTE 保留输入行和返回主键的对应关系，再将子节点按 ordinal 关联到父节点。

### 8.3 Readback

不应继续依赖“写 CTE 后重新读取基表即可看到关系变化”的假设。

更可靠的选择：

1. `RETURNING` 链直接构造结果；或
2. 写入步骤完成后，在同一事务中执行独立 readback query。

第二种多一次数据库命令，但语义更简单、关系结果完整，也能看到事务内前序写入。

## 九、性能优化原则

### 9.1 不要把“一条 SQL”当作所有场景的绝对目标

- Query：单 SQL 通常收益明显，应继续坚持。
- Mutation：顺序、事务、锁和错误语义优先。
- 多表 Action：数据库函数通常是最优单调用方案。
- 远程系统：不能伪装成数据库原子操作。

### 9.2 保留编译计划缓存

建议把计划区分为：

- QueryPlan
- MutationPlan
- PolicyPlan
- ReadbackPlan

缓存键可包含：

```text
schemaVersion + operationText + operationName + role + policyVersion
```

具体变量、scope/session 值继续使用参数槽位，不进入缓存键。

### 9.3 解耦 GORM 元数据与热执行路径

GORM 适合元数据加载和兼容入口，但热执行路径可以抽象为：

```text
PlanExecutor
  QueryRow(ctx, sql, args)
  BeginTx(ctx, options)
```

然后提供：

- database/sql 实现
- pgx 实现
- GORM 适配实现

PostgreSQL 高性能场景可用 pgx 自动 prepared statement cache、Batch 和原生 Tx；不会破坏上层编译器。

### 9.4 成本预算

建议为编译计划计算：

- 字段数量
- relation fan-out 权重
- aggregate 权重
- remote 权重
- recursion 权重
- mutation row/batch 权重
- 估计返回体大小

最终得到可配置的 operation cost，并支持按角色设置不同预算。

## 十、生产能力补全

### 10.1 权限与审计

- 默认拒绝或显式公开策略
- 角色化 schema
- 操作、表、行、列四层权限
- 变更前后审计数据
- 权限拒绝稳定错误码
- 敏感字段默认隐藏

### 10.2 错误模型

建议统一 `extensions.code`：

```text
BAD_USER_INPUT
FORBIDDEN
NOT_FOUND
CONFLICT
CONSTRAINT_VIOLATION
STALE_WRITE
TOO_MANY_ROWS
QUERY_COST_EXCEEDED
INTERNAL_ERROR
```

并保留：

- GraphQL path
- locations
- constraint/table/column 的安全子集
- request/operation trace ID

生产环境不能直接暴露原始 SQL 和数据库内部详情。

### 10.3 可观测性

- parse/validate/compile/cache/execute/resolve 分段耗时
- SQL rows/bytes
- Resolver 和 Remote latency
- plan cache hit ratio
- subscription feed 数与重查次数
- mutation rollback 原因
- slow query sampling
- OpenTelemetry trace
- Explain/Explain Analyze 调试入口

### 10.4 Schema 生命周期

- 元数据和 Schema 版本号
- 数据库结构 drift 检测
- 安全热重载或双缓冲切换
- plan cache 按 schemaVersion 隔离
- persisted document 重新校验
- schema diff 和兼容性检查

### 10.5 可靠事件

CDC 适合订阅唤醒，但业务事件应增加事务 Outbox：

```text
Mutation transaction
  - business rows
  - outbox row
Commit
  - worker/webhook dispatcher
```

这样才能保证业务写入与事件记录一致，并支持重试、幂等和死信处理。

## 十一、建议优先级与实施路线

### 第一阶段：正确性和事务基础

1. 新增统一 MutationPlan + TxExecutor。
2. 顶层 mutation 严格串行。
3. 默认 CRUD 和根 Resolver 共用事务。
4. Resolver 获得 Tx-aware context。
5. 深层嵌套写入暂未支持时明确拒绝，禁止静默忽略。
6. 多行 update 返回 `affectedCount + returning`。
7. 支持同表多个 mutation，使用独立内部别名。
8. 加入 rollback、混合 Resolver、执行顺序和触发器端到端测试。

### 第二阶段：GraphQL 和元数据基础

1. `@skip/@include`。
2. 运行期变量 coercion。
3. 非空冒泡、path/location/extensions。
4. default/generated/identity/unique/check/enum/domain 元数据。
5. `where` 整体变量。
6. 关系过滤和 exists。
7. 类型化 on-conflict constraint。

### 第三阶段：真正零代码业务治理

1. role/session/operation/column/row 权限。
2. insert/update check 与字段 preset。
3. 角色化 Schema 或字段可见性。
4. 数据库函数自动暴露。
5. 声明式多表 Action。
6. 递归嵌套写入图编译器。
7. optimistic concurrency、expected affected rows、idempotency key。

### 第四阶段：生产平台能力

1. SQL cost/复杂度预算，不只限制深度。
2. statement timeout、批量大小、返回字节数限制。
3. Explain/Tracing/Metrics/慢查询。
4. PostgreSQL 错误到稳定 GraphQL code 的映射。
5. Outbox/Event Trigger。
6. Schema drift 检测和安全热重载。
7. APQ/hash allowlist、审计日志。
8. 如确有需求再实现 MySQL Dialect；目前只是 MySQL 元数据 Loader，并没有对应执行编译器。

## 十二、建议的能力边界

最终应明确向使用者承诺以下分层，而不是笼统声称“所有业务零代码”。

| 业务类型 | 推荐实现 | 是否写 Go |
|---|---|---:|
| 普通单表 CRUD | 自动生成 | 否 |
| 常见关系 CRUD | 自动嵌套写入 | 否 |
| 多租户/字段权限 | 声明式 Policy/RLS | 否 |
| 默认值、校验、状态检查 | 约束/声明式 Rule | 否 |
| 高性能多表原子业务 | PostgreSQL Function/Action | 否 |
| 外部系统调用 | Resolver + Outbox/Saga | 少量 |
| 特殊算法和不可声明逻辑 | Typed Resolver | 是 |

合理的最终目标是：

- 80%～90% 常规后台业务只建表和配权限。
- 复杂原子业务主要写数据库函数或声明式 Action。
- 只有外部服务调用和特殊算法才写 Resolver。
- 查询继续维持当前高性能单 SQL 路线。
- Mutation 从“追求一条 SQL”调整为“有序、事务化、可优化的执行计划”。

## 十三、最终建议

最值得保留的是现有查询编译器、参数槽位、计划缓存和字节响应快路径。

下一步不要继续横向堆查询操作符，而应把重心转到三个基础设施：

1. **统一事务化 MutationPlan**
2. **声明式权限与写入检查**
3. **数据库函数/Action 自动暴露**

其中第一项是正确性基础，第二项决定能否安全自动暴露数据库，第三项决定能否真正减少业务代码。

推荐的核心原则是：

> Query 追求单 SQL 和字节快路径；Mutation 追求有序、事务化、可治理；复杂多表逻辑优先下沉数据库函数，Resolver 作为最后逃生口。

## 十四、验证记录

本次审查执行并通过：

```text
go test ./...
go vet ./...
```

测试结果摘要：

- 根包覆盖率：84.4%
- PostgreSQL 编译器覆盖率：80.7%
- Renderer 覆盖率：96.9%
- 全量测试通过
- `go vet` 无问题

报告落地前未修改任何 Go 源码。
