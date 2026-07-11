package main

import (
	"context"
	"strings"
	"testing"

	"github.com/ichaly/ideabase/gql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestResolverExamples(t *testing.T) {
	value, err := greetingResolver().Resolve(context.Background(), gql.Source{"name": "张三"}, map[string]any{"lang": "en"})
	if err != nil || value != "Hello, 张三" {
		t.Fatalf("字段Resolver示例失败: value=%v err=%v", value, err)
	}

	values, err := signResolver().(gql.BatchResolver).ResolveBatch(context.Background(),
		[]map[string]any{{"name": "张三", "email": "zhang@demo.dev"}}, nil)
	if err != nil || len(values) != 1 || values[0] != "张三 <zhang@demo.dev>" {
		t.Fatalf("批量Resolver示例失败: values=%v err=%v", values, err)
	}

	root, err := serverInfoResolver().Resolve(context.Background(), gql.Root{}, nil)
	if err != nil || root.(ServerInfo).Name == "" {
		t.Fatalf("根Resolver示例失败: value=%v err=%v", root, err)
	}

	remote, err := reputationRemote().Fetch(context.Background(), []any{int64(1), int64(2)})
	if err != nil || len(remote) != 2 {
		t.Fatalf("Remote示例失败: values=%v err=%v", remote, err)
	}
}

// TestCompleteDemoDocuments 使用真实Demo库时验证所有命名操作都能通过当前动态schema校验，
// 并验证公开Execute确实返回标准GraphQL字节；无数据库的普通单测环境安全跳过。
func TestCompleteDemoDocuments(t *testing.T) {
	db, err := gorm.Open(postgres.Open(demoDSN()), &gorm.Config{})
	if err != nil {
		t.Skipf("demo数据库不可用: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil || sqlDB.Ping() != nil {
		t.Skip("demo数据库不可用，运行 docker compose up -d 后可执行完整契约测试")
	}
	defer sqlDB.Close()

	executor, err := buildExecutor(db)
	if err != nil {
		t.Fatalf("装配完整Demo失败: %v", err)
	}
	if err = executor.LoadDocuments("./queries"); err != nil {
		t.Fatalf("示例GraphQL文档与动态schema不一致: %v", err)
	}
	body, err := executor.Execute(context.Background(), `query { serverInfo { name version } }`, nil)
	if err != nil {
		t.Fatalf("Execute字节出口失败: %v", err)
	}
	if !strings.Contains(string(body), `"name":"gql-complete-demo"`) {
		t.Fatalf("非标准GraphQL响应: %s", body)
	}

	ctx := gql.WithScope(context.Background(), map[string]any{"tenant": 1})
	checks := []string{
		`{ users { items { id name email greeting sign reputation { level title } } } }`,
		`mutation { echo(message: "hello") }`,
		`{ __schema { queryType { name } } __type(name: "User") { name kind } }`,
	}
	for _, query := range checks {
		body, err = executor.Execute(ctx, query, nil)
		if err != nil || strings.Contains(string(body), `"errors"`) {
			t.Fatalf("扩展能力执行失败: query=%s body=%s err=%v", query, body, err)
		}
	}
}
