package gql

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/compiler/pgsql"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
)

// setupTestExecutor 基于真实PostgreSQL构建完整执行器
func setupTestExecutor(t *testing.T) (*Executor, func()) {
	db, cleanup := setupTestDatabase(t)

	k, err := std.NewKonfig()
	require.NoError(t, err, "创建配置失败")
	k.Set("mode", "dev")
	k.Set("app.root", t.TempDir())
	k.Set("schema.schema", "public")

	meta, err := NewMetadata(k, db)
	require.NoError(t, err, "加载元数据失败")

	compile, err := NewCompiler(meta, []compiler.Dialect{pgsql.NewDialect()})
	require.NoError(t, err, "创建编译器失败")

	executor, err := NewExecutor(db, NewRenderer(meta), meta, compile)
	require.NoError(t, err, "创建执行器失败")

	return executor, cleanup
}

// TestExecutorRoundTrip 端到端冒烟：增删改查 + 关系查询 + 计划缓存
func TestExecutorRoundTrip(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	ctx := context.Background()

	// 1. 创建用户
	reply := executor.Execute(ctx, `mutation {
		createUser(input: { name: "Alice", email: "alice@x.com" }) { id name }
	}`, nil, "")
	require.Empty(t, reply.Errors, "创建用户失败: %v", reply.Errors)
	created := reply.Data["createUser"].(map[string]interface{})
	require.Equal(t, "Alice", created["name"])
	userId := created["id"]
	require.NotNil(t, userId)

	// 2. 创建文章（验证变量参数 + 创建后读回关系）
	reply = executor.Execute(ctx, `mutation ($title: String!, $uid: Int!) {
		createPost(input: { title: $title, userId: $uid }) { id title user { name } }
	}`, map[string]interface{}{"title": "Hello", "uid": userId}, "")
	require.Empty(t, reply.Errors, "创建文章失败: %v", reply.Errors)
	post := reply.Data["createPost"].(map[string]interface{})
	require.Equal(t, "Hello", post["title"])
	require.Equal(t, "Alice", post["user"].(map[string]interface{})["name"])

	// 3. 关系查询（一对多嵌套 + total统计）
	reply = executor.Execute(ctx, `query {
		users(where: { name: { eq: "Alice" } }) {
			items { id name posts { title } }
			total
		}
	}`, nil, "")
	require.Empty(t, reply.Errors, "关系查询失败: %v", reply.Errors)
	users := reply.Data["users"].(map[string]interface{})
	require.EqualValues(t, 1, users["total"])
	items := users["items"].([]interface{})
	require.Len(t, items, 1)
	posts := items[0].(map[string]interface{})["posts"].([]interface{})
	require.Len(t, posts, 1)
	require.Equal(t, "Hello", posts[0].(map[string]interface{})["title"])

	// 4. 更新
	reply = executor.Execute(ctx, `mutation ($id: ID) {
		updateUser(input: { name: "Bob" }, id: $id) { id name }
	}`, map[string]interface{}{"id": userId}, "")
	require.Empty(t, reply.Errors, "更新用户失败: %v", reply.Errors)
	require.Equal(t, "Bob", reply.Data["updateUser"].(map[string]interface{})["name"])

	// 5. 计划缓存：同一查询第二次命中缓存且结果一致
	cached := executor.cache.order.Len()
	reply = executor.Execute(ctx, `query {
		users(where: { name: { eq: "Bob" } }) { items { id name } }
	}`, nil, "")
	require.Empty(t, reply.Errors)
	reply = executor.Execute(ctx, `query {
		users(where: { name: { eq: "Bob" } }) { items { id name } }
	}`, nil, "")
	require.Empty(t, reply.Errors)
	require.Equal(t, cached+1, executor.cache.order.Len(), "重复查询应命中缓存而非新增计划")
	require.Equal(t, "Bob", reply.Data["users"].(map[string]interface{})["items"].([]interface{})[0].(map[string]interface{})["name"])

	// 6. 删除（先删文章再删用户，验证计数返回）
	reply = executor.Execute(ctx, `mutation { deletePost(where: { title: { eq: "Hello" } }) }`, nil, "")
	require.Empty(t, reply.Errors, "删除文章失败: %v", reply.Errors)
	require.EqualValues(t, 1, reply.Data["deletePost"])

	reply = executor.Execute(ctx, `mutation ($id: ID) { deleteUser(id: $id) }`, map[string]interface{}{"id": userId}, "")
	require.Empty(t, reply.Errors, "删除用户失败: %v", reply.Errors)
	require.EqualValues(t, 1, reply.Data["deleteUser"])

	// 7. 删除后查询为空
	reply = executor.Execute(ctx, `query { users { items { id } total } }`, nil, "")
	require.Empty(t, reply.Errors)
	users = reply.Data["users"].(map[string]interface{})
	require.EqualValues(t, 0, users["total"])
	require.Empty(t, users["items"])
}

// TestExecutorDocuments 持久化查询文档：加载、按名执行、未知操作报错
func TestExecutorDocuments(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	ctx := context.Background()

	dir := t.TempDir()
	document := `
		query ListUsers($name: String) {
			users(where: { name: { eq: $name } }) { items { id name } total }
		}
		mutation AddUser($input: UserCreateInput!) {
			createUser(input: $input) { id name }
		}
	`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "user.graphql"), []byte(document), 0644))
	require.NoError(t, executor.LoadDocuments(dir), "加载操作文档失败")

	// 按名执行变更与查询
	reply := executor.ExecuteOperation(ctx, "AddUser", map[string]interface{}{
		"input": map[string]interface{}{"name": "Carol", "email": "c@x.com"},
	})
	require.Empty(t, reply.Errors, "执行持久化变更失败: %v", reply.Errors)
	require.Equal(t, "Carol", reply.Data["createUser"].(map[string]interface{})["name"])

	reply = executor.ExecuteOperation(ctx, "ListUsers", map[string]interface{}{"name": "Carol"})
	require.Empty(t, reply.Errors, "执行持久化查询失败: %v", reply.Errors)
	require.EqualValues(t, 1, reply.Data["users"].(map[string]interface{})["total"])

	// 未知操作名报错
	reply = executor.ExecuteOperation(ctx, "Nope", nil)
	require.NotEmpty(t, reply.Errors)
	require.Contains(t, reply.Errors[0].Message, "未找到")
}
