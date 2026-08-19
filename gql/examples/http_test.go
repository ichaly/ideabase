package main

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ichaly/ideabase/gql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func demoEngine(t *testing.T) *gql.Engine {
	t.Helper()
	db, err := gorm.Open(postgres.Open(demoDSN()), &gorm.Config{})
	if err != nil {
		t.Fatalf("demo数据库不可用: %v（先运行 docker compose up -d）", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取demo数据库连接失败: %v", err)
	}
	if err = sqlDB.Ping(); err != nil {
		t.Fatalf("demo数据库无法连接: %v（先运行 docker compose up -d）", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	engine, err := buildEngine(db)
	if err != nil {
		t.Fatalf("装配Demo失败: %v", err)
	}
	t.Cleanup(engine.Close)
	return engine
}

func TestDemoHTTPExecuteAndPersistedOperation(t *testing.T) {
	server := httptest.NewServer(newDemoHandler(demoEngine(t)))
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/graphql", strings.NewReader(`{"operationName":"CustomRootQuery"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Demo-Tenant", "1")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || strings.Contains(string(body), `"errors"`) || !strings.Contains(string(body), `gql-complete-demo`) {
		t.Fatalf("HTTP持久化操作失败: status=%d body=%s", response.StatusCode, body)
	}
}

func TestDemoHTTPSubscriptionSendsRealSSEEvent(t *testing.T) {
	server := httptest.NewServer(newDemoHandler(demoEngine(t)))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/graphql/stream", strings.NewReader(`{"query":"subscription { users { total } }"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Demo-Tenant", "1")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("SSE握手失败: status=%d content-type=%q body=%s", response.StatusCode, response.Header.Get("Content-Type"), body)
	}

	scanner := bufio.NewScanner(response.Body)
	dataSeen := false
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data: ") {
			dataSeen = true
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("读取SSE失败: %v", err)
	}
	if !dataSeen {
		t.Fatal("SSE未收到真实首个事件")
	}
}

func TestDemoProductionSafetyContract(t *testing.T) {
	db, err := gorm.Open(postgres.Open(demoDSN()), &gorm.Config{})
	if err != nil {
		t.Fatalf("demo数据库不可用: %v（先运行 docker compose up -d）", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err = sqlDB.Ping(); err != nil {
		t.Fatalf("demo数据库无法连接: %v（先运行 docker compose up -d）", err)
	}
	defer sqlDB.Close()

	engine, err := buildEngineMode(db, true)
	if err != nil {
		t.Fatalf("生产安全配置装配失败: %v", err)
	}
	defer engine.Close()
	ctx := demoContext(context.Background(), 1)

	body, err := engine.ExecuteOperation(ctx, "", nil, "CustomRootQuery")
	if err != nil || strings.Contains(string(body), `"errors"`) {
		t.Fatalf("生产 persisted operation 不可执行: body=%s err=%v", body, err)
	}
	body, err = engine.ExecuteOperation(ctx, "", nil, "StandardIntrospection")
	if err == nil && !strings.Contains(string(body), "INTROSPECTION_DISABLED") && !strings.Contains(string(body), `"errors"`) {
		t.Fatalf("生产配置未关闭自省: body=%s", body)
	}

	server := httptest.NewServer(newDemoHandler(engine))
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/graphql", strings.NewReader(`{"query":"{ serverInfo { name } }"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusOK && !strings.Contains(string(responseBody), "PERSISTED") {
		t.Fatalf("生产HTTP接受了非持久化查询: status=%d body=%s", response.StatusCode, responseBody)
	}
}

func TestDemoControlPlaneAndComposedGateways(t *testing.T) {
	server := httptest.NewServer(newDemoHandler(demoEngine(t)))
	defer server.Close()
	get := func(path string, headers map[string]string) (int, string, http.Header) {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, string(body), response.Header
	}
	if status, body, _ := get("/health", nil); status != http.StatusOK || !strings.Contains(body, `"ready":true`) {
		t.Fatalf("health endpoint failed: status=%d body=%s", status, body)
	}
	if status, _, _ := get("/manifest", nil); status != http.StatusUnauthorized {
		t.Fatalf("manifest unauthenticated request was accepted: status=%d", status)
	}
	if status, body, _ := get("/manifest", map[string]string{"X-Demo-Control": "demo"}); status != http.StatusOK || !strings.Contains(body, `"endpointId":"demo"`) {
		t.Fatalf("manifest endpoint failed: status=%d body=%s", status, body)
	}
	if status, body, _ := get("/.well-known/gql-gateway", nil); status != http.StatusOK || !strings.Contains(body, `"version":1`) {
		t.Fatalf("descriptor endpoint failed: status=%d body=%s", status, body)
	}
	if status, _, _ := get("/effective-schema", nil); status != http.StatusUnauthorized {
		t.Fatalf("effective schema unauthenticated request was accepted: status=%d", status)
	}
	if status, body, _ := get("/effective-schema", map[string]string{"Authorization": "GQL-Delegation demo-token"}); status != http.StatusOK || !strings.Contains(body, `"schema":`) {
		t.Fatalf("effective schema endpoint failed: status=%d body=%s", status, body)
	}
	post := func(path, payload string, headers map[string]string) (int, string) {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(body)
	}
	if status, body := post("/gateway/graphql", `{"query":"{ blog { serverInfo { name } } }"}`, nil); status != http.StatusOK || !strings.Contains(body, "gql-complete-demo") {
		t.Fatalf("gateway query failed: status=%d body=%s", status, body)
	}
	if status, body := post("/tenant/graphql", `{"query":"{ serverInfo { name } }"}`, map[string]string{"X-Demo-Tenant": "2"}); status != http.StatusOK || !strings.Contains(body, "gql-complete-demo") {
		t.Fatalf("tenant gateway query failed: status=%d body=%s", status, body)
	}
	if status, body := post("/tenant/graphql", `{"query":"{ serverInfo { name } }"}`, map[string]string{"X-Demo-Tenant": "999"}); status != http.StatusOK || !strings.Contains(body, `"errors"`) || strings.Contains(body, `"data"`) {
		t.Fatalf("unknown tenant was not rejected: status=%d body=%s", status, body)
	}
}

func TestDemoPolicyDenialIsNotAFalseSuccess(t *testing.T) {
	server := httptest.NewServer(newDemoHandler(demoEngine(t)))
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/graphql", strings.NewReader(`{"query":"{ users { total } }"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Demo-Deny", "true")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(body), `"errors"`) || strings.Contains(string(body), `"data":{"users"`) {
		t.Fatalf("policy denial was not observable: status=%d body=%s", response.StatusCode, body)
	}
}

func TestDemoSharedPostgresSecurityAndReplayState(t *testing.T) {
	db, err := gorm.Open(postgres.Open(demoDSN()), &gorm.Config{})
	if err != nil {
		t.Fatalf("demo数据库不可用: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err = sqlDB.Ping(); err != nil {
		t.Fatalf("demo数据库无法连接: %v", err)
	}
	if err = db.Exec(gql.PostgresReplayDDL).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Exec(gql.PostgresSecurityStateDDL).Error; err != nil {
		t.Fatal(err)
	}
	first, err := gql.NewPostgresSecurityStateProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	second, err := gql.NewPostgresSecurityStateProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	currentRevision, err := first.Revision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	activeRevision := currentRevision + 1
	if err = first.Replace(context.Background(), activeRevision, map[string]gql.TenantSecurityState{"1": {}}); err != nil {
		t.Fatal(err)
	}
	if err = second.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if revision, err := second.Revision(context.Background()); err != nil || revision != activeRevision {
		t.Fatalf("second provider did not observe shared revision: revision=%d err=%v", revision, err)
	}
	if err = first.Replace(context.Background(), activeRevision+1, map[string]gql.TenantSecurityState{"1": {Disabled: true}}); err != nil {
		t.Fatal(err)
	}
	if err = second.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	identity, err := gql.NewIdentity(gql.IdentityInput{Tenant: "1", Subject: "demo-user"})
	if err != nil {
		t.Fatal(err)
	}
	err = second.Check(context.Background(), identity, gql.TenantRoute{Tenant: "1", Revision: 1})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("second provider accepted revoked tenant: %v", err)
	}
	one, err := gql.NewPostgresReplayStore(db)
	if err != nil {
		t.Fatal(err)
	}
	two, err := gql.NewPostgresReplayStore(db)
	if err != nil {
		t.Fatal(err)
	}
	key := gql.ReplayKey{Issuer: "demo", Audience: "demo", ID: "shared-token"}
	if err = one.Consume(context.Background(), key, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = two.Consume(context.Background(), key, time.Now().Add(time.Minute)); err == nil {
		t.Fatal("second replay store accepted a token consumed by the first store")
	}
}
