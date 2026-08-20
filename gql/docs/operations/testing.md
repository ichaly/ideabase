# 运维：测试与性能

## 全量测试

```bash
./scripts/test-all.sh
```

脚本会删除旧 PostgreSQL volume、启动干净 fixture、等待健康后运行 `GOWORK=off go test ./... -count=1`，退出时清理容器。`GQL_TEST_KEEP_DB=1` 可在失败排查时保留 fixture。

## 真实 Demo 门禁

```bash
./examples/scripts/test-demo.sh
```

它覆盖所有持久化 operation、真实 HTTP 启动、SSE、Remote → Gateway、多副本 replay/security、撤权传播和 PostgreSQL 性能预算。

## 静态、并发和模糊测试

```bash
go vet ./...
go test -race ./... -count=1
go test . -run '^$' -fuzz '^FuzzExecutableFrontend$' -fuzztime=10s
```

## 性能门禁

```bash
./scripts/perf.sh allocations
go test . -run '^$' -bench . -benchmem -count=10
go test ./compiler/pgsql -run '^$' -bench . -benchmem -count=10
```

GA 重点不是跨机器绝对 ns，而是稳定的分配预算、SQL 次数、缓存命中、nested 单 SQL、Batch 单次调用和拒绝路径零 SQL；P50/P95/P99 在固定 runner 上比较。

## 发布前检查

- [ ] Schema、Capability、Effective Schema 与执行一致
- [ ] 每个公开 operation 有成功、拒绝和边界测试
- [ ] 多副本共享 Replay/Security/Audit 状态
- [ ] migration、CDC、DB 权限 preflight 通过
- [ ] full、race、fuzz、Demo E2E 和性能门禁通过
- [ ] 文档示例和 `examples/` 能复现

---

[← 生产部署](production.md) · [文档首页](../README.md)
