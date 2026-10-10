package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// This gate includes JWT, tenant routing, RLS, the pool, SQL, JSON and HTTP.
// Set ORDERS_SOAK_SECONDS=300 for the release soak; ordinary tests run for 3s.
func TestOrderPopulatedHTTPWorkload(t *testing.T) {
	e, db := testEndpoint(t)
	_, err := db.Pool().Exec(`UPDATE products SET stock=10000;
 INSERT INTO orders(tenant_id,customer_id,request_id,request_body,created_by,updated_by)
 SELECT CASE WHEN n%2=0 THEN 'a' ELSE 'b' END, CASE WHEN n%2=0 THEN 1 ELSE 2 END,
 'seed-'||n,'{}','seed','seed' FROM generate_series(1,100000) n;
 INSERT INTO order_items(tenant_id,order_id,product_id,quantity,price_cents)
 SELECT tenant_id,id,CASE WHEN tenant_id='a' THEN 1 ELSE 3 END,1,100 FROM orders;
 ANALYZE orders; ANALYZE order_items; ANALYZE customers; ANALYZE products;`)
	require.NoError(t, err)
	query := `{ orders(limit:20,sort:{createdAt:DESC}) { items { id status customer { name } orderItems { quantity product { name } } } } }`
	initial := execute(t, e, "a", query)
	require.Empty(t, initial["errors"], "%v", initial)
	require.Len(t, initial["data"].(map[string]any)["orders"].(map[string]any)["items"], 20)
	count := execute(t, e, "a", `{ orders { total } }`)
	require.EqualValues(t, 50000, count["data"].(map[string]any)["orders"].(map[string]any)["total"])
	h, err := newHTTPHandler(e, testSecret)
	require.NoError(t, err)
	server := httptest.NewServer(h)
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	tokens := []string{token(t, "a", "operator", time.Now().Add(time.Hour)), token(t, "b", "operator", time.Now().Add(time.Hour))}
	seconds := 3
	if raw := os.Getenv("ORDERS_SOAK_SECONDS"); raw != "" {
		seconds, err = strconv.Atoi(raw)
		require.NoError(t, err)
		require.GreaterOrEqual(t, seconds, 1)
	}
	var mu sync.Mutex
	var durations []time.Duration
	var failures []string
	call := func(worker int, query string) map[string]any {
		payload, _ := json.Marshal(map[string]string{"query": query})
		req, _ := http.NewRequest("POST", server.URL+"/graphql", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tokens[worker%2])
		start := time.Now()
		response, err := client.Do(req)
		var result map[string]any
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			err = readErr
			if err == nil {
				err = json.Unmarshal(body, &result)
			}
			if err == nil && (response.StatusCode != 200 || result["errors"] != nil) {
				err = fmt.Errorf("status=%d body=%s", response.StatusCode, body)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		durations = append(durations, time.Since(start))
		if err != nil && len(failures) < 10 {
			failures = append(failures, query+": "+err.Error())
		}
		return result
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	deadline := start.Add(time.Duration(seconds) * time.Second)
	var wg sync.WaitGroup
	for worker := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; time.Now().Before(deadline); n++ {
				if n%32 != 0 {
					call(worker, query)
					continue
				}
				customer, product := 1, 1
				if worker%2 == 1 {
					customer, product = 2, 3
				}
				reply := call(worker, fmt.Sprintf(`mutation { placeOrder(requestId:"load-%d-%d",customerId:%d,items:[{productId:%d,quantity:1}]) { id } }`, worker, n, customer, product))
				if data, ok := reply["data"].(map[string]any); ok {
					if receipt, ok := data["placeOrder"].(map[string]any); ok {
						call(worker, fmt.Sprintf(`mutation { cancelOrder(id:%q) { status } }`, receipt["id"]))
					}
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	require.NotEmpty(t, durations)
	slices.Sort(durations)
	p := func(percent int) time.Duration { return durations[(len(durations)-1)*percent/100] }
	t.Logf("orders HTTP: rows=100000 items=100000 tenants=2 workers=16 requests=%d duration=%s rps=%.1f p50=%s p95=%s p99=%s heapBefore=%d heapAfter=%d gc=%d", len(durations), elapsed, float64(len(durations))/elapsed.Seconds(), p(50), p(95), p(99), before.HeapAlloc, after.HeapAlloc, after.NumGC-before.NumGC)
	require.Empty(t, failures, strings.Join(failures, "\n"))
	require.Less(t, p(99), time.Second, "mixed workload latency budget")
	require.Greater(t, float64(len(durations))/elapsed.Seconds(), 20.0, "conservative shared CI throughput floor")
	var stock, unfinished int
	require.NoError(t, db.Pool().QueryRow(`SELECT sum(stock) FROM products`).Scan(&stock))
	require.Equal(t, 30000, stock, "all load orders must restore stock")
	require.NoError(t, db.Pool().QueryRow(`SELECT count(*) FROM orders WHERE request_id LIKE 'load-%' AND status<>'cancelled'`).Scan(&unfinished))
	require.Zero(t, unfinished)
}
