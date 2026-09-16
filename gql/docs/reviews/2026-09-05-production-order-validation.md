# GQL 生产订单验证与发布边界

日期：2026-09-05。基线：`c42f9d7610d16b502d9e04ef82e160d6befd475a`。
本报告补充并取代历史 GA 报告对当前工作树的就绪度判断。

## 使用与设计

目标是数据库设计加少量业务 Resolver 即可提供管理后台 API。新增 [订单示例](../../examples/orders/README.md)：数据库生成客户维护、客户/商品/订单/明细查询；下单和取消各一个 Resolver。
身份认证使用已有 JWT 库，权限、租户路由、请求事务、订阅、远程组合和副本安全复用引擎。
查询、突变、SSE 共用 `/graphql`；`/health` 用于部署探针，远程 manifest/effective-schema 属于实例组合的控制接口。

应用无需声明第二套业务模型或执行器。所有副本、测试和可执行程序调用同一 `buildEndpoint`，连接池固定为 16 个连接，空闲保留 16 个；这是本示例的容量配置，不是引擎的全局最优值。
同一租户 requestId 保证下单幂等，复合外键和强制 RLS 约束租户关系，按商品主键顺序加锁避免交叉购物篮的反向锁序。

## 修复的生产问题

- 生产模式此前隐式依赖生成的 metadata 文件；现在连接数据库即从 catalog 构建。显式 `metadata.file` 仍是唯一权威来源，文件错误拒绝启动。
- 重用配置对象构建多个实例时，默认文件路径不再被误当成显式 artifact。
- CDC 可复用迁移账号预建的 publication，运行时复制账号不再需要 CREATE publication 权限；publication 完整性校验保留。
- 不可变实例可通过有效 Schema 的结构指纹证明远程请求版本；可变 Gateway 继续选择保留快照，错误版本拒绝执行。
- 示例的 JWT 到期会取消请求上下文，已建立的 SSE 同时结束。

## 验证记录

**判断：当前版本通过下述代码发布验收，可用于 PostgreSQL 管理 API 的实际生产部署，部署须满足本文前提。** 这是限定支持范围的工程判断，不是长期运行或任意拓扑的保证；没有将程序部署到用户生产环境，也没有声称远端 CI 已运行。

引擎生产 Go 文件 85 个、26,476 行（含注释/空行，不含测试、examples 和 tools），比基线少 165 行；其中五轮精简净减 170 行，生产问题修复净增 5 行。
订单示例的业务代码 `orders.go` 为 159 行，另有装配 98 行、认证 50 行、启动程序 133 行，以及数据库 schema；不能把 159 行误认为完整应用总代码量。

| 验证 | 结果 |
| --- | --- |
| Go 1.26.8，`GOWORK=off go test -race ./... -count=1`，独立 PG16 原有示例库 | 全模块通过 |
| `go vet ./...`、tools 模块测试、`git diff --check` | 通过 |
| PostgreSQL 15/17/18，核心包及订单完整测试 | 均通过；测试自动创建独立库 |
| `GQL_RUN_CLUSTER_GA=1`，核心跨进程、HTTP Remote/Gateway、SQL 数量/并发门禁 | 通过 |
| frontend fuzz 10 秒 | 通过，241,691 次执行 |
| `govulncheck ./...` | 0 个可达漏洞；仍报告 3 个 imported / 9 个 required 不可达信号 |
| 编译后 CLI 实跑 | 空库初始化、受限账号、readiness、JWT、下单/查询/取消、SSE、活动订阅下 SIGTERM 均通过 |
| 5 分钟真实 HTTP 负载 | 915,068 请求，3,050.2 请求/秒；P50 4.82ms，P95 8.35ms，P99 11.72ms；0 请求失败 |

持续负载使用本机 Apple M1 Max、Docker PostgreSQL 16，初始 10 万订单和 10 万明细、2 个租户、16 个 worker。每个 worker 每 32 轮执行一对下单/取消，其余为嵌套关系查询；查询与写入均含 JWT、租户路由、RLS、真实 SQL 和 HTTP。
结束时库存恢复到 30,000，所有负载订单均为 cancelled。堆分配前后为 3,634,136 / 17,450,536 字节，后值未强制 GC 且包含压测收集的延迟样本，不能据此判断泄漏。

最终微基准：基线 85 个生产文件与 `c42f9d7` 逐文件核对；同机按基线、当前版本顺序运行，`GOMAXPROCS=1`、每项 10 次、200ms。现有 perfgate 的 10% 显著耗时退化门禁通过。

| 基准 | 前→后 ns/op 中位数 | 前→后 allocs/op |
| --- | ---: | ---: |
| ReplyBytes | 1,617 → 1,626 | 1 → 1 |
| Hosts | 1,050 → 1,046 | 10 → 10 |
| GatewayWarmQuery | 6,046 → 5,899.5 | 76 → 76 |
| ObserverDisabled | 2.114 → 2.144 | 0 → 0 |
| CompileSimple | 3,170.5 → 2,980.5 | 22 → 22 |
| CompileNested | 8,021 → 7,869 | 51 → 51 |
| CompileMutation/relation | 13,475 → 12,524 | 133 → 113 |
| CompileMutation/variables | 4,893.5 → 4,701.5 | 64 → 62 |

复现命令见订单 README 与 `.github/workflows/gql-ga.yml`。本地原始日志保留在 `/tmp/orders-release-{race,cluster,vuln,fuzz,soak-300,benchmark}.log`、`/tmp/orders-release-pg{15,17,18}.log`、`/tmp/orders-cli-release.log` 和 `/tmp/orders-release-bench-{base,head}.txt`；这些临时日志不随仓库分发，以上结果为本次摘要。

曾有一次 5 分钟负载失败。诊断发现测试装配沿用默认连接池（无限打开、仅 2 个空闲），持续连接创建出现 EOF；可执行程序与测试配置不一致。
连接池移入共同装配后，60 秒验证通过：185,043 次请求，约 3,084 请求/秒，P99 11.14ms，库存全部回补。没有添加重试掩盖失败，也没有放宽通过条件。

## 复查

按用户 AGENTS 规约在主线程顺序检查复用、质量、效率以及改动的正确性、安全、契约、并发与测试。没有独立模型审查，不将自查表述为独立认可。
Code review: skipped (ce-code-review unavailable)
原因：本会话要求子代理任务映射到主线程，未执行该技能的独立审查调度；已显式执行人工方式的逐项 diff 扫描。

复查保留租户过滤、RLS、身份/版本校验、排序稳定性、事务提交后通知及资源上限。修正生产配置文档矛盾与启动命令，补强 SSE 初始事件断言，CDC 测试清理所建角色。
本轮复用/质量/效率精简新增代码变更均为 0：已完成的五轮精简保留，不再为行数额外增加策略层或替换已经验证的组件。

## 部署与回滚

- 先用迁移账号执行应用 DDL、所需安全/重放/审计表与 publication，再启动受限应用和复制账号；`-migrate` 仅用于初始化空库。
- 配置可信登录签发 JWT、共享安全状态、租户路由和实际容量限制；外部流量经 TLS。远程组合实例共享 replay store。
- 升级前检查 [MIGRATION.md](../../MIGRATION.md)：删除公开 Go 包装符号属于源码不兼容；显式配置旧 metadata artifact 路径或授权读取 catalog。
- 滚动发布保持数据库兼容，重新获取结构指纹和 execution pin；发生错误率或隔离异常时回滚程序，保留兼容的数据及治理表。

### Post-Deploy Monitoring & Validation

部署负责人在首个灰度租户发布后观察至少 30 分钟，再扩大流量。检查健康探针 ready、CDC 连接、连接池等待、HTTP/GraphQL 错误率、P95/P99、撤权传播、数据库锁等待及 CPU。
查询日志中的 `EXECUTION_ERROR`、`security state unavailable`、`SNAPSHOT_CHANGED`、CDC 重连与限流记录。正常信号是健康持续通过、库存不为负、重复请求不重复扣减、越租户访问被拒绝，延迟满足该部署预先设定的 SLO。
出现越租户数据、重复扣减或撤权失效立即停止扩大并回滚；错误率/延迟持续超出既定 SLO 或 CDC 无法收敛时也回滚或减流。跨版本数据迁移按应用的 expand/contract 流程处理。

## 能力边界

本次重点支持 PostgreSQL 的数据库驱动管理 API。MySQL catalog 支持不等于 MySQL 执行/CDC 全功能 GA。
CDC 是 latest-state 查询流，不是 durable event replay；跨 namespace Mutation 没有分布式事务。
自动生成能力取决于约束、关系、索引、权限与业务规则的数据库设计质量；该引擎不包含管理后台 UI。
本机短期负载不能证明世界最高性能、长期无泄漏或任意部署容量。真正生产容量仍须在目标硬件、数据分布、网络、备份和 SLO 下验收。
