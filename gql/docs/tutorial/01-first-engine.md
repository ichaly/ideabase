# 教程：创建第一个 Engine

## 目标

本章会连接一个 PostgreSQL 数据库，构建 Engine，并通过标准 HTTP 端点执行一条查询。

## 前置条件

- Go 1.26.5 或更高版本
- PostgreSQL 15 或更高版本
- 一个有 catalog 读取权限的数据库用户

## 1. 安装依赖

```bash
go get github.com/ichaly/ideabase/gql
go get gorm.io/driver/postgres gorm.io/gorm
```

## 2. 连接数据库并构建

```go
package main

import (
    "log"
    "github.com/ichaly/ideabase/gql"
    "github.com/ichaly/ideabase/std"
    "gorm.io/driver/postgres"
    "gorm.io/gorm"
)

func main() {
    db, err := gorm.Open(postgres.Open("host=localhost user=app dbname=app sslmode=disable"), &gorm.Config{})
    if err != nil { log.Fatal(err) }

    config, err := std.NewKonfig()
    if err != nil { log.Fatal(err) }
    config.Set("schema.introspection", true)
    config.Set("schema.default-limit", 20)

    engine, err := gql.New(config, db).Build()
    if err != nil { log.Fatal(err) }
    defer engine.Close()
}
```

`Build` 会读取数据库元数据、生成 Schema、冻结 capability、创建编译器和执行器。任何元数据、策略、安全或迁移错误都会让 Build 失败，不会返回半可用实例。

## 3. 暴露 HTTP

```go
mux := http.NewServeMux()
mux.Handle("/graphql", transport.NewHandler(engine, transport.Options{}))
mux.Handle("/graphql/stream", transport.NewSubscriptionHandler(engine, transport.Options{}))
log.Fatal(http.ListenAndServe(":8080", mux))
```

执行：

```bash
curl http://localhost:8080/graphql \
  -H 'Content-Type: application/json' \
  -d '{"query":"{ users { items { id name } } }"}'
```

## 4. 查看生成 Schema

开发环境打开 `schema.introspection` 后，使用标准 introspection 查询查看实体、输入、分页、关系和自定义字段。生产环境可关闭 introspection，改用受认证保护的 Manifest 和 Effective Schema 端点。

## 验证

仓库自带环境：

```bash
cd gql
./scripts/test-all.sh
```

下一步：[第一次查询和 Mutation →](02-first-query.md)

---

[← 文档首页](../README.md) · [下一章 →](02-first-query.md)
