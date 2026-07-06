// 端到端性能基准：复用demo实际装配（buildExecutor），打真实PostgreSQL。
// 前提与e2e一致——demo数据库需在运行（docker compose up -d，暴露5433）；
// 不可达时跳过。运行：go test -bench=. -benchmem -run=^$
package main

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/ichaly/ideabase/gql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// benchExecutor 连demo库并装配引擎；库不可达则跳过基准
func benchExecutor(b *testing.B) *gql.Executor {
	db, err := gorm.Open(postgres.Open(demoDSN()), &gorm.Config{})
	if err != nil {
		b.Skipf("demo数据库不可用（docker compose up -d）: %v", err)
	}
	if sqlDB, err := db.DB(); err != nil || sqlDB.Ping() != nil {
		b.Skip("demo数据库无法连接（docker compose up -d）")
	}
	executor, err := buildExecutor(db)
	if err != nil {
		b.Fatalf("引擎装配失败: %v", err)
	}
	return executor
}

// runQuery 经公开 HTTP 入口端到端跑一条查询并校验无错（计划缓存首次后命中，测稳态吞吐）
func runQuery(b *testing.B, query string) {
	executor := benchExecutor(b)
	app := fiber.New()
	executor.Bind(app.Group(executor.Path()))
	body := `{"query":` + strconv.Quote(query) + `}`
	do := func() {
		req := httptest.NewRequest("POST", executor.Path(), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil || resp.StatusCode != 200 {
			b.Fatalf("查询出错: err=%v status=%v", err, resp)
		}
	}
	do() // 预热编译缓存
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		do()
	}
}

// BenchmarkFlatQuery 平铺查询：单表无关系，测per-request固定开销（编译缓存命中+执行+组织）
func BenchmarkFlatQuery(b *testing.B) {
	runQuery(b, `{ posts(limit: 20) { items { id title } } }`)
}

// BenchmarkNestedQuery 嵌套关系查询：posts->user(多对一)+tags(多对多)。
// 整棵树编译为单条SQL，一次往返——体现"任意深度零N+1"
func BenchmarkNestedQuery(b *testing.B) {
	runQuery(b, `{ posts(limit: 20) { items { id title user { name } tags { name } } } }`)
}

// BenchmarkResolverBatch 带BatchResolver字段（sign）：整页宿主一次ResolveBatch调用，
// 对比同样N行——验证resolver不产生N+1放大
func BenchmarkResolverBatch(b *testing.B) {
	runQuery(b, `{ users(limit: 20) { items { name email sign } } }`)
}
