# 快速开始

## 前置条件

Go 1.26.5+、PostgreSQL 15+、GORM PostgreSQL driver。CDC 订阅需要 PostgreSQL `wal_level=logical`。

## 组装 Engine

```go
db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
if err != nil { log.Fatal(err) }
cfg, err := std.NewKonfig()
if err != nil { log.Fatal(err) }
cfg.Set("schema.introspection", true)
cfg.Set("schema.default-limit", 20)

builder := gql.New(cfg, db)
// 可选：builder.Register(...), Replace, Wrap, LoadDocuments, Policy, Observe
engine, err := builder.Build()
if err != nil { log.Fatal(err) }
defer engine.Close()
```

生产 PostgreSQL Engine 必须显式配置 `SecurityStateProvider`，并通过 migration preflight。Build 失败时不会返回半可用 Engine。

## HTTP 接入

```go
mux := http.NewServeMux()
mux.Handle("/graphql", transport.NewHandler(engine, transport.Options{}))
mux.Handle("/graphql/stream", transport.NewSubscriptionHandler(engine, transport.Options{}))
log.Fatal(http.ListenAndServe(":8080", mux))
```

```bash
curl http://localhost:8080/graphql \
  -H 'Content-Type: application/json' \
  -d '{"query":"{ users { items { id name } } }"}'
```

## Build 前注册

```go
builder.Register(resolver, batchResolver, remote)
builder.Replace(existingResolver)
builder.Wrap("User.greeting", middleware)
builder.LoadDocuments("./operations")
builder.Policy(policyProvider)
builder.Observe(observer)
```

注册表在第一次 Build 后冻结。metadata permissions 和显式 `PolicyProvider` 只能二选一。

## 验证

```bash
./scripts/test-all.sh
./examples/scripts/test-demo.sh
```

---

[← 返回目录](README.md) · [下一章：数据库映射与 CRUD →](02-data-and-operations.md)
