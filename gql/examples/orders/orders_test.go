package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/ichaly/ideabase/std"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ichaly/ideabase/gql"
	_ "github.com/ichaly/ideabase/gql/compiler/pgsql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

var adminDSN, appDSN, cdcDSN string

func TestMain(m *testing.M) {
	// A private disposable database: tests never reset a developer's database.
	out, err := exec.Command("docker", "run", "--rm", "-d", "-p", "127.0.0.1::5432", "-e", "POSTGRES_PASSWORD=fixture", testPostgresImage(), "-c", "wal_level=logical").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "postgres: %v %s\n", err, out)
		os.Exit(1)
	}
	id := strings.TrimSpace(string(out))
	code := func() int {
		defer exec.Command("docker", "rm", "-f", id).Run()
		out, err = exec.Command("docker", "port", id, "5432/tcp").Output()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		port := strings.TrimSpace(strings.Split(string(out), ":")[1])
		adminDSN = "host=127.0.0.1 port=" + port + " user=postgres password=fixture dbname=postgres sslmode=disable"
		appDSN = "host=127.0.0.1 port=" + port + " user=orders_app password=fixture dbname=postgres sslmode=disable"
		cdcDSN = "host=127.0.0.1 port=" + port + " user=orders_cdc password=fixture dbname=postgres sslmode=disable"
		pool, err := sql.Open("pgx", adminDSN)
		if err != nil {
			return 1
		}
		defer pool.Close()
		deadline := time.Now().Add(20 * time.Second)
		for pool.Ping() != nil {
			if time.Now().After(deadline) {
				return 1
			}
			time.Sleep(100 * time.Millisecond)
		}
		schema, err := os.ReadFile("schema.sql")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		_, err = pool.Exec(string(schema) + gql.PostgresSecurityStateDDL + gql.PostgresReplayDDL + `
   CREATE ROLE orders_app LOGIN PASSWORD 'fixture' NOSUPERUSER NOBYPASSRLS;
   CREATE ROLE orders_cdc LOGIN PASSWORD 'fixture' REPLICATION NOSUPERUSER NOBYPASSRLS;
   GRANT SELECT ON ALL TABLES IN SCHEMA public TO orders_cdc;
   GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA public TO orders_app;
   GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO orders_app;
   UPDATE gql_security_state SET revision=1, tenants='{"a":{},"b":{}}';
   CREATE PUBLICATION ideabase_cdc FOR ALL TABLES;
  `)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		result := m.Run()
		if result != 0 {
			logs, _ := exec.Command("docker", "logs", id).CombinedOutput()
			_ = os.WriteFile("/tmp/orders-postgres-failure.log", logs, 0600)
		}
		return result
	}()
	os.Exit(code)
}

func testEndpoint(t *testing.T) (*gql.TenantGateway, *sql.DB) {
	t.Helper()
	admin, err := sql.Open("pgx", adminDSN)
	require.NoError(t, err)
	t.Cleanup(func() { admin.Close() })
	_, err = admin.Exec(`TRUNCATE order_items, orders, products, customers RESTART IDENTITY CASCADE;
 INSERT INTO customers(tenant_id,name) VALUES ('a','Alice'),('b','Bob');
 INSERT INTO products(tenant_id,name,price_cents,stock) VALUES ('a','Tea',100,10),('a','Coffee',200,2),('b','Tea',150,10);`)
	require.NoError(t, err)
	pool, err := sql.Open("pgx", appDSN)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })
	db, err := std.WrapConnection(pool, "postgres")
	require.NoError(t, err)
	engine, gateway, err := buildEndpoint(db, cdcDSN, []string{"a", "b"}, nil)
	require.NoError(t, err)
	t.Cleanup(engine.Close)
	return gateway, admin
}
func identity(tenant string) context.Context {
	id, err := gql.NewIdentity(gql.IdentityInput{Tenant: tenant, Subject: "operator", Roles: []string{"operator"}})
	if err != nil {
		panic(err)
	}
	return gql.WithScope(gql.WithIdentity(context.Background(), id), map[string]any{"tenant": tenant})
}
func execute(t *testing.T, e gql.Endpoint, tenant, query string) map[string]any {
	t.Helper()
	body, err := e.ExecuteOperation(identity(tenant), query, nil, "")
	require.NoError(t, err)
	var reply map[string]any
	require.NoError(t, json.Unmarshal(body, &reply))
	return reply
}
func TestOrderLifecycle(t *testing.T) {
	e, db := testEndpoint(t)
	reply := execute(t, e, "a", `mutation { placeOrder(requestId:"first",customerId:1,items:[{productId:1,quantity:2}]) { id status } }`)
	require.Empty(t, reply["errors"], "%v", reply)
	var stock int
	require.NoError(t, db.QueryRow(`SELECT stock FROM products WHERE id=1`).Scan(&stock))
	require.Equal(t, 8, stock)
	id := reply["data"].(map[string]any)["placeOrder"].(map[string]any)["id"].(string)
	for range 2 {
		cancelled := execute(t, e, "a", fmt.Sprintf(`mutation { cancelOrder(id:%q) { id status } }`, id))
		require.Empty(t, cancelled["errors"], "%v", cancelled)
		require.Equal(t, "cancelled", cancelled["data"].(map[string]any)["cancelOrder"].(map[string]any)["status"])
	}
	require.NoError(t, db.QueryRow(`SELECT stock FROM products WHERE id=1`).Scan(&stock))
	require.Equal(t, 10, stock)
}

func TestOrderRetryAndRollback(t *testing.T) {
	e, db := testEndpoint(t)
	query := `mutation { placeOrder(requestId:"retry",customerId:1,items:[{productId:1,quantity:2}]) { id status } }`
	first := execute(t, e, "a", query)
	require.Empty(t, first["errors"])
	second := execute(t, e, "a", query)
	require.Equal(t, first, second)
	require.NotEmpty(t, execute(t, e, "a", strings.Replace(query, "quantity:2", "quantity:3", 1))["errors"])
	for _, query := range []string{
		`mutation { placeOrder(requestId:"stock",customerId:1,items:[{productId:1,quantity:1},{productId:2,quantity:3}]) { id } }`,
		`mutation { placeOrder(requestId:"customer",customerId:2,items:[{productId:1,quantity:1}]) { id } }`,
		`mutation { placeOrder(requestId:"product",customerId:1,items:[{productId:3,quantity:1}]) { id } }`,
		`mutation { placeOrder(requestId:"duplicate",customerId:1,items:[{productId:1,quantity:1},{productId:1,quantity:1}]) { id } }`,
		`mutation { placeOrder(requestId:"negative",customerId:1,items:[{productId:1,quantity:-1}]) { id } }`,
	} {
		require.NotEmpty(t, execute(t, e, "a", query)["errors"], query)
	}
	var orders, stock int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM orders`).Scan(&orders))
	require.Equal(t, 1, orders)
	require.NoError(t, db.QueryRow(`SELECT stock FROM products WHERE id=1`).Scan(&stock))
	require.Equal(t, 8, stock)
	id := first["data"].(map[string]any)["placeOrder"].(map[string]any)["id"].(string)
	require.NotEmpty(t, execute(t, e, "b", fmt.Sprintf(`mutation { cancelOrder(id:%q) { id } }`, id))["errors"])
	hidden := execute(t, e, "b", `{ orders { items { id } total } }`)
	require.Empty(t, hidden["errors"])
	require.EqualValues(t, 0, hidden["data"].(map[string]any)["orders"].(map[string]any)["total"])
	require.NotEmpty(t, execute(t, e, "a", `mutation { updateProduct(id:1,input:{stock:100}) { id } }`)["errors"])
}

func TestOrderReadOnlyRoleCannotWrite(t *testing.T) {
	e, _ := testEndpoint(t)
	id, err := gql.NewIdentity(gql.IdentityInput{Tenant: "a", Subject: "reader", Roles: []string{"viewer"}})
	require.NoError(t, err)
	ctx := gql.WithScope(gql.WithIdentity(context.Background(), id), map[string]any{"tenant": "a"})
	body, err := e.ExecuteOperation(ctx, `mutation { placeOrder(requestId:"forbidden",customerId:1,items:[{productId:1,quantity:1}]) { id } }`, nil, "")
	require.NoError(t, err)
	require.Contains(t, string(body), `"errors"`)
}

func TestOrderReplicasCannotOversell(t *testing.T) {
	first, db := testEndpoint(t)
	pool, err := sql.Open("pgx", appDSN)
	require.NoError(t, err)
	defer pool.Close()
	replicaDB, err := std.WrapConnection(pool, "postgres")
	require.NoError(t, err)
	engine, second, err := buildEndpoint(replicaDB, cdcDSN, []string{"a", "b"}, nil)
	require.NoError(t, err)
	defer engine.Close()
	results := make(chan bool, 24)
	for i := range 24 {
		go func() {
			endpoint := first
			if i%2 == 1 {
				endpoint = second
			}
			body, err := endpoint.ExecuteOperation(identity("a"), fmt.Sprintf(`mutation { placeOrder(requestId:"race-%d",customerId:1,items:[{productId:1,quantity:1}]) { id } }`, i), nil, "")
			results <- err == nil && !strings.Contains(string(body), `"errors"`)
		}()
	}
	successes := 0
	for range 24 {
		if <-results {
			successes++
		}
	}
	require.Equal(t, 10, successes)
	var stock, count int
	require.NoError(t, db.QueryRow(`SELECT stock FROM products WHERE id=1`).Scan(&stock))
	require.Zero(t, stock)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM orders`).Scan(&count))
	require.Equal(t, 10, count)
}

func TestOrderConcurrentRetryCreatesOnce(t *testing.T) {
	e, db := testEndpoint(t)
	results := make(chan string, 12)
	for range 12 {
		go func() {
			body, err := e.ExecuteOperation(identity("a"), `mutation { placeOrder(requestId:"same",customerId:1,items:[{productId:1,quantity:2}]) { id status } }`, nil, "")
			if err != nil {
				results <- err.Error()
			} else {
				results <- string(body)
			}
		}()
	}
	first := <-results
	require.NotContains(t, first, `"errors"`)
	for range 11 {
		require.Equal(t, first, <-results)
	}
	var stock, count int
	require.NoError(t, db.QueryRow(`SELECT stock FROM products WHERE id=1`).Scan(&stock))
	require.Equal(t, 8, stock)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM orders`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestOrderCompositionAndCDC(t *testing.T) {
	e, _ := testEndpoint(t)
	composed, err := gql.NewGateway("admin", gql.Namespace{Name: "sales", EndpointID: "sales", Endpoint: e})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(identity("a"), 10*time.Second)
	defer cancel()
	stream, err := e.Subscribe(ctx, `subscription { products(where:{id:{eq:1}}) { items { stock } } }`, nil, "")
	require.NoError(t, err)
	select {
	case body := <-stream:
		require.Contains(t, string(body), `"stock":10`)
	case <-ctx.Done():
		t.Fatal("initial subscription timed out")
	}
	reply := execute(t, composed, "a", `mutation { sales_placeOrder(requestId:"composed",customerId:1,items:[{productId:1,quantity:2}]) { id } }`)
	require.Empty(t, reply["errors"], "%v", reply)
	select {
	case body := <-stream:
		require.Contains(t, string(body), `"stock":8`)
	case <-ctx.Done():
		t.Fatal("committed order did not refresh subscription")
	}
}

func testPostgresImage() string {
	if image := os.Getenv("GQL_TEST_POSTGRES_IMAGE"); image != "" {
		return image
	}
	return "postgres:16-alpine"
}

func TestOrderGeneratedCustomerManagement(t *testing.T) {
	e, _ := testEndpoint(t)
	reply := execute(t, e, "a", `mutation { createCustomer(input:{name:"New customer"}) { id name } }`)
	require.Empty(t, reply["errors"], "%v", reply)
	id := reply["data"].(map[string]any)["createCustomer"].(map[string]any)["id"]
	query := fmt.Sprintf(`mutation { updateCustomer(id:%q,input:{name:"Renamed"}) { name } }`, fmt.Sprint(id))
	reply = execute(t, e, "a", query)
	require.Empty(t, reply["errors"])
	require.Equal(t, "Renamed", reply["data"].(map[string]any)["updateCustomer"].(map[string]any)["name"])
}

func TestOrderAssemblyBoundsConnectionPool(t *testing.T) {
	pool, err := sql.Open("pgx", appDSN)
	require.NoError(t, err)
	defer pool.Close()
	db, err := std.WrapConnection(pool, "postgres")
	require.NoError(t, err)
	engine, _, err := buildEndpoint(db, cdcDSN, []string{"a", "b"}, nil)
	require.NoError(t, err)
	defer engine.Close()
	require.Equal(t, 16, pool.Stats().MaxOpenConnections, "tests and executable must share pool configuration")
}
