package gql

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm/logger"
)

type gaSQLCounter struct{ count atomic.Int64 }

func (c *gaSQLCounter) LogMode(logger.LogLevel) logger.Interface      { return c }
func (c *gaSQLCounter) Info(context.Context, string, ...interface{})  {}
func (c *gaSQLCounter) Warn(context.Context, string, ...interface{})  {}
func (c *gaSQLCounter) Error(context.Context, string, ...interface{}) {}
func (c *gaSQLCounter) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	fc()
	c.count.Add(1)
}

func TestGAPerformanceRealPostgresMutationSQLGate(t *testing.T) {
	if os.Getenv("GQL_RUN_CLUSTER_GA") != "1" {
		t.Skip("set GQL_RUN_CLUSTER_GA=1 to run PostgreSQL performance GA")
	}
	executor, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	counter := &gaSQLCounter{}
	db.Logger = counter
	query := executor.run(context.Background(), `{ users { items { id posts { id } } total } }`, nil, "")
	require.Empty(t, query.Errors, "%v", query.Errors)
	require.EqualValues(t, 1, counter.count.Load(), "nested read must stay one SQL statement")

	beforeFlat := counter.count.Load()
	flat := executor.run(context.Background(), `mutation { createUser(input: {name: "GA flat", email: "ga-flat@example.test"}) { id } }`, nil, "")
	require.Empty(t, flat.Errors, "%v", flat.Errors)
	require.EqualValues(t, 1, counter.count.Load()-beforeFlat, "flat mutation must stay one SQL statement")

	beforeNested := counter.count.Load()
	nested := executor.run(context.Background(), `mutation { createUser(input: {name: "GA nested", email: "ga-nested@example.test", posts: {create: [{title: "child"}]}}) { id } }`, nil, "")
	require.Empty(t, nested.Errors, "%v", nested.Errors)
	require.EqualValues(t, 1, counter.count.Load()-beforeNested, "nested mutation must stay one SQL statement")

	beforeRejected := counter.count.Load()
	rejected := executor.run(context.Background(), `mutation { createUser(input: {name: "bad"}) { doesNotExist } }`, nil, "")
	require.NotEmpty(t, rejected.Errors)
	require.Equal(t, beforeRejected, counter.count.Load(), "rejected operation must execute zero SQL")
}

func TestGAPerformanceRealPostgresConcurrentWarmGate(t *testing.T) {
	if os.Getenv("GQL_RUN_CLUSTER_GA") != "1" {
		t.Skip("set GQL_RUN_CLUSTER_GA=1 to run PostgreSQL performance GA")
	}
	executor, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	counter := &gaSQLCounter{}
	db.Logger = counter
	const concurrency, rounds = 8, 8
	query := `{ users { items { id posts { id } } total } }`
	// Warm the compiler/metadata path before measuring the database workload.
	warm := executor.run(context.Background(), query, nil, "")
	require.Empty(t, warm.Errors, "%v", warm.Errors)
	counter.count.Store(0)
	durations := make([]time.Duration, 0, concurrency*rounds)
	var mu sync.Mutex
	var failures atomic.Int64
	start := time.Now()
	var group sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for i := 0; i < rounds; i++ {
				began := time.Now()
				result := executor.run(context.Background(), query, nil, "")
				if len(result.Errors) != 0 {
					failures.Add(1)
					continue
				}
				mu.Lock()
				durations = append(durations, time.Since(began))
				mu.Unlock()
			}
		}()
	}
	group.Wait()
	elapsed := time.Since(start)
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	require.EqualValues(t, 0, failures.Load(), "error rate must be zero")
	require.Len(t, durations, concurrency*rounds)
	if len(durations) == 0 {
		t.Fatal("warm workload produced no samples")
	}
	require.EqualValues(t, int64(concurrency*rounds), counter.count.Load(), "warm query must issue exactly one SQL per request")
	p := func(q int) time.Duration { return durations[(len(durations)-1)*q/100] }
	throughput := float64(len(durations)) / elapsed.Seconds()
	fmt.Printf("GA postgres warm: concurrency=%d requests=%d elapsed=%s throughput=%.1f/s p50=%s p95=%s p99=%s sql=%d errors=%d\n", concurrency, len(durations), elapsed, throughput, p(50), p(95), p(99), counter.count.Load(), failures.Load())
	// Keep enough room for shared CI, while rejecting an order-of-magnitude
	// regression against the warm real-Postgres baseline (~2.3k req/s, ~15ms).
	require.GreaterOrEqual(t, throughput, 500.0, "warm throughput regression budget")
	require.LessOrEqual(t, p(99), 100*time.Millisecond, "warm p99 latency regression budget")
	allocs := testing.AllocsPerRun(3, func() {
		result := executor.run(context.Background(), query, nil, "")
		if len(result.Errors) != 0 {
			t.Fatalf("allocation sample failed: %v", result.Errors)
		}
	})
	fmt.Printf("GA postgres warm: allocations/request=%.0f\n", allocs)
	require.LessOrEqual(t, allocs, float64(100), "allocation budget")
	before := counter.count.Load()
	rejected := executor.run(context.Background(), `mutation { createUser(input: {name: "bad"}) { doesNotExist } }`, nil, "")
	require.NotEmpty(t, rejected.Errors)
	require.Equal(t, before, counter.count.Load(), "rejected operation must issue zero SQL")
}
