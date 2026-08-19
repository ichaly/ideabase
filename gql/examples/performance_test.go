package main

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// demoSQLCounter records database round trips without changing the production
// logger. It is used only by the real-PostgreSQL Demo performance gate.
type demoSQLCounter struct{ count atomic.Int64 }

func (c *demoSQLCounter) LogMode(logger.LogLevel) logger.Interface      { return c }
func (c *demoSQLCounter) Info(context.Context, string, ...interface{})  {}
func (c *demoSQLCounter) Warn(context.Context, string, ...interface{})  {}
func (c *demoSQLCounter) Error(context.Context, string, ...interface{}) {}
func (c *demoSQLCounter) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	fc()
	c.count.Add(1)
}

// TestDemoPerformanceGate is a repeatable, real-PostgreSQL regression gate.
// It intentionally checks hard invariants (one SQL and zero errors) plus a
// conservative latency/throughput budget; detailed numbers remain available
// from BenchmarkFlatQuery/BenchmarkNestedQuery.
func TestDemoPerformanceGate(t *testing.T) {
	db, err := openDemoDB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	engine, err := buildEngine(db)
	if err != nil {
		t.Fatalf("装配Demo失败: %v", err)
	}
	defer engine.Close()

	counter := &demoSQLCounter{}
	db.Logger = counter
	ctx := demoContext(context.Background(), 1)
	query := `{ posts(limit: 20) { items { id title user { name } tags { name } } total } }`
	warm, err := engine.Execute(ctx, query, nil)
	if err != nil {
		t.Fatalf("性能门禁预热失败: %v body=%s", err, warm)
	}
	counter.count.Store(0)
	one, err := engine.Execute(ctx, query, nil)
	if err != nil {
		t.Fatalf("性能门禁单请求失败: %v body=%s", err, one)
	}
	if counter.count.Load() != 1 {
		t.Fatalf("嵌套查询产生了 %d 条SQL，期望单条SQL", counter.count.Load())
	}

	const workers, rounds = 8, 8
	durations := make([]time.Duration, 0, workers*rounds)
	var mu sync.Mutex
	var failures atomic.Int64
	started := time.Now()
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for i := 0; i < rounds; i++ {
				began := time.Now()
				body, executeErr := engine.Execute(ctx, query, nil)
				if executeErr != nil {
					failures.Add(1)
					continue
				}
				mu.Lock()
				durations = append(durations, time.Since(began))
				mu.Unlock()
				if len(body) == 0 {
					failures.Add(1)
				}
			}
		}()
	}
	group.Wait()
	elapsed := time.Since(started)
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	if failures.Load() != 0 || len(durations) != workers*rounds {
		t.Fatalf("并发性能门禁失败: requests=%d failures=%d", len(durations), failures.Load())
	}
	if got := counter.count.Load(); got != int64(workers*rounds+1) {
		t.Fatalf("并发嵌套查询SQL数=%d，期望=%d", got, workers*rounds+1)
	}
	percentile := func(percent int) time.Duration {
		return durations[(len(durations)-1)*percent/100]
	}
	throughput := float64(len(durations)) / elapsed.Seconds()
	t.Logf("demo PostgreSQL warm: concurrency=%d requests=%d throughput=%.1f/s p50=%s p95=%s p99=%s sql=%d", workers, len(durations), throughput, percentile(50), percentile(95), percentile(99), counter.count.Load())
	if throughput < 50 {
		t.Fatalf("并发吞吐低于保守门槛: %.1f req/s", throughput)
	}
	if percentile(99) > 500*time.Millisecond {
		t.Fatalf("并发P99超过保守门槛: %s", percentile(99))
	}
}

func openDemoDB() (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(demoDSN()), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("demo数据库不可用（先运行 docker compose up -d）: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取demo数据库连接失败: %w", err)
	}
	if err = sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("demo数据库无法连接（先运行 docker compose up -d）: %w", err)
	}
	return db, nil
}
