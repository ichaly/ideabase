# 生产与测试

## 配置与迁移

```yaml
schema:
  file: ./cfg/schema.graphql
  introspection: false
  persisted-only: true
  default-limit: 50
  max-depth: 12
  max-cost: 100000
subscription:
  publication: ideabase_cdc
```

启动必须完成 metadata/schema/policy 校验、replay/security/audit/outbox migration preflight、数据库权限检查和 CDC readiness。配置错误不能降级成开放权限 Engine。

## 测试入口

```bash
./scripts/test-all.sh                 # 重建 PostgreSQL fixture 后全量测试
./examples/scripts/test-demo.sh       # Demo、HTTP、Remote、两副本、撤权、性能
go vet ./...
go test -race ./... -count=1
go test . -run '^$' -fuzz '^FuzzExecutableFrontend$' -fuzztime=10s
```

`GQL_TEST_KEEP_DB=1 ./scripts/test-all.sh` 可保留 fixture 排查失败。

## 性能门禁

```bash
./scripts/perf.sh allocations
go test . -run '^$' -bench . -benchmem -count=10
go test ./compiler/pgsql -run '^$' -bench . -benchmem -count=10
```

硬门禁应检查分配预算、SQL 次数、缓存命中、flat/nested 单 SQL、Batch Resolver 单次调用和拒绝路径零 SQL；真实 PostgreSQL 压测再观察吞吐、P50/P95/P99。

## 发布清单

- [ ] Schema、Capability、Effective Schema 与执行一致
- [ ] 租户、字段、操作和行策略均有测试
- [ ] 多副本共享 Replay/Security/Audit 状态
- [ ] migration、CDC、数据库权限 preflight 通过
- [ ] 全量、race、fuzz、Demo E2E 和性能门禁通过
- [ ] 没有未文档化的特殊 GraphQL 指令或隐藏能力
- [ ] 示例可从 `examples/` 复现

---

[← 上一章](05-gateway-and-transport.md) · [返回目录](README.md)
