package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ichaly/ideabase/gql"
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

	remote, err := reputationResolver().(gql.BatchResolver).ResolveBatch(context.Background(), []gql.Source{{"__rk_id": int64(1)}, {"__rk_id": int64(2)}}, nil)
	if err != nil || len(remote) != 2 {
		t.Fatalf("按键Resolver示例失败: values=%v err=%v", remote, err)
	}
}

// TestCompleteDemoDocuments 使用真实Demo库逐个执行所有持久化操作；数据库不可用直接失败。
func TestCompleteDemoDocuments(t *testing.T) {
	db, err := openDemoDB()
	if err != nil {
		t.Fatalf("demo数据库不可用: %v（先运行 docker compose up -d）", err)
	}
	defer db.Pool().Close()

	executor, err := buildEngine(db)
	if err != nil {
		t.Fatalf("装配完整Demo失败: %v", err)
	}
	defer executor.Close()
	ctx := demoContext(context.Background(), 1)
	bytesBody, err := executor.Execute(ctx, `{ serverInfo { name version } }`, nil)
	if err != nil || !strings.Contains(string(bytesBody), `gql-complete-demo`) || strings.Contains(string(bytesBody), `"errors"`) {
		t.Fatalf("Engine.Execute字节快路径失败: body=%s err=%v", bytesBody, err)
	}
	created := executeNamed(t, executor, ctx, "CrudCreate", map[string]any{
		"name": "全能力Demo用户", "email": "complete-demo@example.test",
	})
	createdID := nestedString(t, created, "data", "createUser", "id")
	executeNamed(t, executor, ctx, "CrudUpdate", map[string]any{"id": createdID, "name": "全能力Demo用户已更新"})
	executeNamed(t, executor, ctx, "CrudDelete", map[string]any{"id": createdID})

	for _, operation := range []struct {
		name string
		vars map[string]any
	}{
		{"BatchCreate", nil},
		{"UpsertByUnique", nil},
		{"NestedCreate", nil},
		{"NestedConnectDisconnect", nil},
		{"GeneratedPrimaryKeys", nil},
		{"AddTag", map[string]any{"name": "Demo新增标签"}},
		{"PostList", map[string]any{"kw": "数据库"}},
		{"FilterSortOffset", map[string]any{"limit": 2, "offset": 0}},
		{"ForwardCursorPage", nil},
		{"BackwardCursorPage", nil},
		{"JsonDistinctRelations", nil},
		{"AggregateHaving", nil},
		{"RecursiveComments", nil},
		{"TenantScopedPosts", nil},
		{"ResolverAndRemote", map[string]any{"lang": "en"}},
		{"CustomRootQuery", nil},
		{"CustomRootMutation", map[string]any{"message": "hello"}},
		{"StandardIntrospection", nil},
	} {
		executeNamed(t, executor, ctx, operation.name, operation.vars)
	}
}

func executeNamed(t *testing.T, executor *gql.Engine, ctx context.Context, name string, variables map[string]any) map[string]any {
	t.Helper()
	body, err := executor.ExecuteOperation(ctx, "", variables, name)
	if err != nil {
		t.Fatalf("持久化操作 %s 执行失败: %v", name, err)
	}
	var reply map[string]any
	if err = json.Unmarshal(body, &reply); err != nil {
		t.Fatalf("持久化操作 %s 返回非法JSON: %v body=%s", name, err, body)
	}
	if _, ok := reply["errors"]; ok {
		t.Fatalf("持久化操作 %s 返回GraphQL errors: %s", name, body)
	}
	if _, ok := reply["data"]; !ok {
		t.Fatalf("持久化操作 %s 缺少data: %s", name, body)
	}
	return reply
}

func nestedString(t *testing.T, value map[string]any, path ...string) string {
	t.Helper()
	var current any = value
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("响应路径 %s 不是对象: %#v", strings.Join(path, "."), current)
		}
		current, ok = object[segment]
		if !ok {
			t.Fatalf("响应缺少路径 %s: %#v", strings.Join(path, "."), value)
		}
	}
	result, ok := current.(string)
	if !ok || result == "" {
		t.Fatalf("响应路径 %s 不是非空字符串: %#v", strings.Join(path, "."), current)
	}
	return result
}
