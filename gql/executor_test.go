package gql

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/ichaly/ideabase/gql/compiler/pgsql" // 自注册PostgreSQL方言
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

	// 走方言自注册路径（导入pgsql包即注册）
	compile, err := NewCompiler(meta, nil)
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

// TestExecutorRelationOps 嵌套写入真库验证：创建挂接、多对多connect/disconnect原子完成
func TestExecutorRelationOps(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	ctx := context.Background()

	run := func(query string, vars map[string]interface{}) map[string]interface{} {
		reply := executor.Execute(ctx, query, vars, "")
		require.Empty(t, reply.Errors, "执行失败: %v", reply.Errors)
		return reply.Data
	}

	// 既有数据：作者 + 两篇游离文章 + 两个标签
	author := run(`mutation { createUser(input: { name: "Au", email: "au@x.com" }) { id } }`, nil)["createUser"].(map[string]interface{})["id"]
	orphanA := run(`mutation ($u: Int!) { createPost(input: { title: "PA", userId: $u }) { id } }`,
		map[string]interface{}{"u": author})["createPost"].(map[string]interface{})["id"]
	_ = run(`mutation ($u: Int!) { createPost(input: { title: "PB", userId: $u }) { id } }`, map[string]interface{}{"u": author})
	tag1 := run(`mutation { createTag(input: { name: "t1" }) { id } }`, nil)["createTag"].(map[string]interface{})["id"]
	tag2 := run(`mutation { createTag(input: { name: "t2" }) { id } }`, nil)["createTag"].(map[string]interface{})["id"]

	// 创建新作者并原子挂接既有文章PA（一对多connect=改外键）
	// 注：PG快照语义下同语句读回看不到关系变更，用后续查询验证
	owner := run(`mutation ($p: ID!) {
		createUser(input: { name: "New", email: "new@x.com", posts: { connect: [$p] } }) { id }
	}`, map[string]interface{}{"p": orphanA})["createUser"].(map[string]interface{})

	data := run(`query ($id: ID) { users(id: $id) { items { posts { title } } } }`,
		map[string]interface{}{"id": owner["id"]})
	posts := data["users"].(map[string]interface{})["items"].([]interface{})[0].(map[string]interface{})["posts"].([]interface{})
	require.Len(t, posts, 1)
	require.Equal(t, "PA", posts[0].(map[string]interface{})["title"])

	// 多对多connect：文章挂两个标签（原子，单语句）
	run(`mutation ($id: ID, $t1: ID!, $t2: ID!) {
		updatePost(input: { tags: { connect: [$t1, $t2] } }, id: $id) { id }
	}`, map[string]interface{}{"id": orphanA, "t1": tag1, "t2": tag2})
	data = run(`query ($id: ID) { posts(id: $id) { items { tags { name } } } }`, map[string]interface{}{"id": orphanA})
	tags := data["posts"].(map[string]interface{})["items"].([]interface{})[0].(map[string]interface{})["tags"].([]interface{})
	require.Len(t, tags, 2)

	// disconnect解除一个
	run(`mutation ($id: ID, $t: ID!) {
		updatePost(input: { tags: { disconnect: [$t] } }, id: $id) { id }
	}`, map[string]interface{}{"id": orphanA, "t": tag1})
	data = run(`query ($id: ID) { posts(id: $id) { items { tags { name } } } }`, map[string]interface{}{"id": orphanA})
	tags = data["posts"].(map[string]interface{})["items"].([]interface{})[0].(map[string]interface{})["tags"].([]interface{})
	require.Len(t, tags, 1)
	require.Equal(t, "t2", tags[0].(map[string]interface{})["name"])
}

// TestExecutorCursor 游标分页真库验证：向前逐页遍历 + 向后取末页
func TestExecutorCursor(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	ctx := context.Background()

	for _, name := range []string{"A", "B", "C", "D", "E"} {
		reply := executor.Execute(ctx, `mutation ($n: String!, $e: String!) {
			createUser(input: { name: $n, email: $e }) { id }
		}`, map[string]interface{}{"n": name, "e": name + "@x.com"}, "")
		require.Empty(t, reply.Errors, "准备数据失败: %v", reply.Errors)
	}

	// 向前遍历：每页2条，应得 [A,B] [C,D] [E]
	var pages [][]string
	cursor := interface{}(nil)
	for i := 0; i < 5; i++ { // 上限防死循环
		reply := executor.Execute(ctx, `query ($c: Cursor) {
			users(first: 2, after: $c, sort: { name: ASC }) {
				items { name }
				pageInfo { hasNext hasPrev end }
			}
		}`, map[string]interface{}{"c": cursor}, "")
		require.Empty(t, reply.Errors, "翻页失败: %v", reply.Errors)

		users := reply.Data["users"].(map[string]interface{})
		var names []string
		for _, item := range users["items"].([]interface{}) {
			names = append(names, item.(map[string]interface{})["name"].(string))
		}
		pages = append(pages, names)

		info := users["pageInfo"].(map[string]interface{})
		require.Equal(t, cursor != nil, info["hasPrev"], "hasPrev应反映是否携带游标")
		if info["hasNext"] != true {
			break
		}
		cursor = info["end"]
		require.NotNil(t, cursor, "有下一页时end游标不应为空")
	}
	require.Equal(t, [][]string{{"A", "B"}, {"C", "D"}, {"E"}}, pages)

	// 向后取末页：last: 2 应得 [D,E]（显示顺序）且hasPrev=true
	reply := executor.Execute(ctx, `query {
		users(last: 2, sort: { name: ASC }) { items { name } pageInfo { hasNext hasPrev } }
	}`, nil, "")
	require.Empty(t, reply.Errors, "向后翻页失败: %v", reply.Errors)
	users := reply.Data["users"].(map[string]interface{})
	var names []string
	for _, item := range users["items"].([]interface{}) {
		names = append(names, item.(map[string]interface{})["name"].(string))
	}
	require.Equal(t, []string{"D", "E"}, names)
	info := users["pageInfo"].(map[string]interface{})
	require.Equal(t, true, info["hasPrev"])
	require.Equal(t, false, info["hasNext"])
}

// TestExecutorStats 统计聚合真库验证：全表聚合与分组聚合
func TestExecutorStats(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	ctx := context.Background()

	for _, m := range []string{
		`mutation { createUser(input: { name: "A", email: "a@x.com" }) { id } }`,
		`mutation { createUser(input: { name: "B", email: "b@x.com" }) { id } }`,
		`mutation { createUser(input: { name: "B", email: "b2@x.com" }) { id } }`,
	} {
		reply := executor.Execute(ctx, m, nil, "")
		require.Empty(t, reply.Errors, "准备数据失败: %v", reply.Errors)
	}

	// 全表聚合
	reply := executor.Execute(ctx, `query { userStats { count email { countDistinct } } }`, nil, "")
	require.Empty(t, reply.Errors, "全表聚合失败: %v", reply.Errors)
	rows := reply.Data["userStats"].([]interface{})
	require.Len(t, rows, 1)
	row := rows[0].(map[string]interface{})
	require.EqualValues(t, 3, row["count"])
	require.EqualValues(t, 3, row["email"].(map[string]interface{})["countDistinct"])

	// 分组聚合
	reply = executor.Execute(ctx, `query { userStats(groupBy: ["name"], where: { name: { eq: "B" } }) { key count } }`, nil, "")
	require.Empty(t, reply.Errors, "分组聚合失败: %v", reply.Errors)
	rows = reply.Data["userStats"].([]interface{})
	require.Len(t, rows, 1)
	row = rows[0].(map[string]interface{})
	require.EqualValues(t, 2, row["count"])
	require.Equal(t, "B", row["key"].(map[string]interface{})["name"])
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
