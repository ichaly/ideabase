package gql

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/ichaly/ideabase/gql/compiler/pgsql" // 自注册PostgreSQL方言
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"gorm.io/gorm"
)

// newTestExecutor 基于真实PostgreSQL构建完整执行器（各测试的唯一构造入口）；
// tweak 在元数据构建前调整配置，opts 传递元数据选项（如WithCodecs）
func newTestExecutor(t *testing.T, tweak func(*std.Konfig), opts ...MetadataOption) (*Executor, *gorm.DB, func()) {
	db, cleanup := setupTestDatabase(t)

	k, err := std.NewKonfig()
	require.NoError(t, err, "创建配置失败")
	k.Set("mode", "dev")
	k.Set("app.root", t.TempDir())
	k.Set("schema.schema", "public")
	if tweak != nil {
		tweak(k)
	}

	meta, err := NewMetadata(k, db, opts...)
	require.NoError(t, err, "加载元数据失败")

	// 走方言自注册路径（导入pgsql包即注册）
	compile, err := NewCompiler(meta, nil)
	require.NoError(t, err, "创建编译器失败")

	executor, err := NewExecutor(db, NewRenderer(meta), meta, compile)
	require.NoError(t, err, "创建执行器失败")

	return executor, db, cleanup
}

// setupTestExecutor 默认配置的执行器
func setupTestExecutor(t *testing.T) (*Executor, func()) {
	executor, _, cleanup := newTestExecutor(t, nil)
	return executor, cleanup
}

// run 是测试侧的对象化辅助：生产API只返回字节，需要断言Data时由测试自行解码。
func (my *Executor) run(ctx context.Context, query string, variables map[string]interface{}, operationName string) gqlReply {
	body, err := my.executeBytes(ctx, query, variables, operationName)
	if err != nil {
		return gqlReply{Errors: gqlerror.List{gqlerror.Wrap(err)}}
	}
	var reply gqlReply
	if err = jsonNumeric.Unmarshal(body, &reply); err != nil {
		return gqlReply{Errors: gqlerror.List{gqlerror.Wrap(err)}}
	}
	normalizeNumbers(reply.Data)
	return reply
}

func TestExecuteReturnsGraphQLBytes(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()

	out, err := executor.Execute(context.Background(), `query { users { total } }`, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"users":{"total":0}}}`, string(out))
}

// TestExecutorRoundTrip 端到端冒烟：增删改查 + 关系查询 + 计划缓存
func TestExecutorRoundTrip(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	ctx := context.Background()

	// 1. 创建用户
	reply := executor.run(ctx, `mutation {
		createUser(input: { name: "Alice", email: "alice@x.com" }) { id name }
	}`, nil, "")
	require.Empty(t, reply.Errors, "创建用户失败: %v", reply.Errors)
	created := reply.Data["createUser"].(map[string]interface{})
	require.Equal(t, "Alice", created["name"])
	userId := created["id"]
	require.NotNil(t, userId)

	// 2. 创建文章（验证变量参数 + 创建后读回关系）
	reply = executor.run(ctx, `mutation ($title: String!, $uid: ID!) {
		createPost(input: { title: $title, userId: $uid }) { id title user { name } }
	}`, map[string]interface{}{"title": "Hello", "uid": userId}, "")
	require.Empty(t, reply.Errors, "创建文章失败: %v", reply.Errors)
	post := reply.Data["createPost"].(map[string]interface{})
	require.Equal(t, "Hello", post["title"])
	require.Equal(t, "Alice", post["user"].(map[string]interface{})["name"])

	// 3. 关系查询（一对多嵌套 + total统计）
	reply = executor.run(ctx, `query {
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
	reply = executor.run(ctx, `mutation ($id: ID) {
		updateUser(input: { name: "Bob" }, id: $id) { id name }
	}`, map[string]interface{}{"id": userId}, "")
	require.Empty(t, reply.Errors, "更新用户失败: %v", reply.Errors)
	require.Equal(t, "Bob", reply.Data["updateUser"].(map[string]interface{})["name"])

	// 5. 计划缓存：同一查询第二次命中缓存且结果一致
	cached := executor.cache.order.Len()
	reply = executor.run(ctx, `query {
		users(where: { name: { eq: "Bob" } }) { items { id name } }
	}`, nil, "")
	require.Empty(t, reply.Errors)
	reply = executor.run(ctx, `query {
		users(where: { name: { eq: "Bob" } }) { items { id name } }
	}`, nil, "")
	require.Empty(t, reply.Errors)
	require.Equal(t, cached+1, executor.cache.order.Len(), "重复查询应命中缓存而非新增计划")
	require.Equal(t, "Bob", reply.Data["users"].(map[string]interface{})["items"].([]interface{})[0].(map[string]interface{})["name"])

	// 6. 删除（先删文章再删用户，验证计数返回）
	reply = executor.run(ctx, `mutation { deletePost(where: { title: { eq: "Hello" } }) }`, nil, "")
	require.Empty(t, reply.Errors, "删除文章失败: %v", reply.Errors)
	require.EqualValues(t, 1, reply.Data["deletePost"])

	reply = executor.run(ctx, `mutation ($id: ID) { deleteUser(id: $id) }`, map[string]interface{}{"id": userId}, "")
	require.Empty(t, reply.Errors, "删除用户失败: %v", reply.Errors)
	require.EqualValues(t, 1, reply.Data["deleteUser"])

	// 7. 删除后查询为空
	reply = executor.run(ctx, `query { users { items { id } total } }`, nil, "")
	require.Empty(t, reply.Errors)
	users = reply.Data["users"].(map[string]interface{})
	require.EqualValues(t, 0, users["total"])
	require.Empty(t, users["items"])
}

// TestIntroFallback 自省特征误命中字面量时应回退到正常数据查询路径
func TestIntroFallback(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()

	reply := executor.run(context.Background(), `query {
		users(where: { name: { eq: "__schema demo" } }) { items { id } total }
	}`, nil, "")
	require.Empty(t, reply.Errors, "应回退为数据查询: %v", reply.Errors)
	require.EqualValues(t, 0, reply.Data["users"].(map[string]interface{})["total"])
}

// TestExecutorTree 递归全树真库验证：三层评论链 A->B->C
func TestExecutorTree(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	ctx := context.Background()

	run := func(query string, vars map[string]interface{}) map[string]interface{} {
		reply := executor.run(ctx, query, vars, "")
		require.Empty(t, reply.Errors, "执行失败: %v", reply.Errors)
		return reply.Data
	}

	uid := run(`mutation { createUser(input: { name: "U", email: "u@x.com" }) { id } }`, nil)["createUser"].(map[string]interface{})["id"]
	pid := run(`mutation ($u: ID!) { createPost(input: { title: "P", userId: $u }) { id } }`,
		map[string]interface{}{"u": uid})["createPost"].(map[string]interface{})["id"]

	make := func(content string, parent interface{}) interface{} {
		in := map[string]interface{}{"content": content, "userId": uid, "postId": pid}
		if parent != nil {
			in["parentId"] = parent
		}
		return run(`mutation ($in: CommentCreateInput!) { createComment(input: $in) { id } }`,
			map[string]interface{}{"in": in})["createComment"].(map[string]interface{})["id"]
	}
	a := make("A", nil)
	b := make("B", a)
	c := make("C", b)

	// 全部后代：A下应有B、C
	data := run(`query ($id: ID) { comments(id: $id) { items { descendants(sort: { content: ASC }) { content } } } }`,
		map[string]interface{}{"id": a})
	desc := data["comments"].(map[string]interface{})["items"].([]interface{})[0].(map[string]interface{})["descendants"].([]interface{})
	require.Len(t, desc, 2)
	require.Equal(t, "B", desc[0].(map[string]interface{})["content"])
	require.Equal(t, "C", desc[1].(map[string]interface{})["content"])

	// 限深1：只有B
	data = run(`query ($id: ID) { comments(id: $id) { items { descendants(depth: 1) { content } } } }`,
		map[string]interface{}{"id": a})
	desc = data["comments"].(map[string]interface{})["items"].([]interface{})[0].(map[string]interface{})["descendants"].([]interface{})
	require.Len(t, desc, 1)
	require.Equal(t, "B", desc[0].(map[string]interface{})["content"])

	// 祖先链：C向上应有B、A
	data = run(`query ($id: ID) { comments(id: $id) { items { ancestors(sort: { content: ASC }) { content } } } }`,
		map[string]interface{}{"id": c})
	anc := data["comments"].(map[string]interface{})["items"].([]interface{})[0].(map[string]interface{})["ancestors"].([]interface{})
	require.Len(t, anc, 2)
	require.Equal(t, "A", anc[0].(map[string]interface{})["content"])
}

// TestExecutorDistinct distinct真库验证
func TestExecutorDistinct(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	ctx := context.Background()

	reply := executor.run(ctx, `mutation { createUsers(input: [
		{ name: "X", email: "x1@x.com" }, { name: "X", email: "x2@x.com" }, { name: "Y", email: "y@x.com" }
	]) { id } }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)

	reply = executor.run(ctx, `query { users(distinct: ["name"]) { items { name } } }`, nil, "")
	require.Empty(t, reply.Errors, "distinct查询失败: %v", reply.Errors)
	items := reply.Data["users"].(map[string]interface{})["items"].([]interface{})
	require.Len(t, items, 2, "按name去重应得2行")
}

// TestExecutorBulk 批量插入/upsert/嵌套创建真库验证
func TestExecutorBulk(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	ctx := context.Background()

	run := func(query string, vars map[string]interface{}) map[string]interface{} {
		reply := executor.run(ctx, query, vars, "")
		require.Empty(t, reply.Errors, "执行失败: %v", reply.Errors)
		return reply.Data
	}

	// 批量创建（变量数组形态）
	data := run(`mutation ($in: [UserCreateInput!]!) { createUsers(input: $in) { id name } }`,
		map[string]interface{}{"in": []interface{}{
			map[string]interface{}{"name": "A", "email": "a@x.com"},
			map[string]interface{}{"name": "B", "email": "b@x.com"},
		}})
	require.Len(t, data["createUsers"].([]interface{}), 2)

	// upsert：email冲突更新name，新email插入
	data = run(`mutation { upsertUsers(input: [
		{ name: "A2", email: "a@x.com" },
		{ name: "C", email: "c@x.com" }
	], on: ["email"]) { name email } }`, nil)
	require.Len(t, data["upsertUsers"].([]interface{}), 2)
	users := run(`query { users(sort: { name: ASC }) { items { name } total } }`, nil)["users"].(map[string]interface{})
	require.EqualValues(t, 3, users["total"], "upsert应更新1条插入1条")
	first := users["items"].([]interface{})[0].(map[string]interface{})["name"]
	require.Equal(t, "A2", first, "冲突行name应被更新")

	// 嵌套创建：建用户同时内联建两篇文章（同语句原子）
	owner := run(`mutation { createUser(input: {
		name: "D", email: "d@x.com",
		posts: { create: [{ title: "N1" }, { title: "N2" }] }
	}) { id } }`, nil)["createUser"].(map[string]interface{})
	posts := run(`query ($id: ID) { users(id: $id) { items { posts { title } } } }`,
		map[string]interface{}{"id": owner["id"]})["users"].(map[string]interface{})["items"].([]interface{})[0].(map[string]interface{})["posts"].([]interface{})
	require.Len(t, posts, 2, "内联创建的子行应挂在新用户名下")

	// m2m内联创建：更新文章时内联建新标签并关联
	postId := run(`mutation ($u: ID!) { createPost(input: { title: "M", userId: $u }) { id } }`,
		map[string]interface{}{"u": owner["id"]})["createPost"].(map[string]interface{})["id"]
	run(`mutation ($id: ID) { updatePost(input: { tags: { create: [{ name: "newtag" }] } }, id: $id) { id } }`,
		map[string]interface{}{"id": postId})
	tags := run(`query ($id: ID) { posts(id: $id) { items { tags { name } } } }`,
		map[string]interface{}{"id": postId})["posts"].(map[string]interface{})["items"].([]interface{})[0].(map[string]interface{})["tags"].([]interface{})
	require.Len(t, tags, 1)
	require.Equal(t, "newtag", tags[0].(map[string]interface{})["name"])
}

// TestExecutorSearch 全文搜索真库验证：自动探测pg_trgm，中文子串检索与相关度排序
func TestExecutorSearch(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	k, err := std.NewKonfig()
	require.NoError(t, err)
	k.Set("mode", "dev")
	k.Set("app.root", t.TempDir())
	k.Set("schema.schema", "public")
	k.Set("metadata.classes", map[string]*internal.ClassConfig{
		"Post": {Table: "posts", Search: []string{"title", "content"}},
	})

	meta, err := NewMetadata(k, db)
	require.NoError(t, err)
	compile, err := NewCompiler(meta, nil)
	require.NoError(t, err)
	executor, err := NewExecutor(db, NewRenderer(meta), meta, compile)
	require.NoError(t, err)

	// 官方镜像无jieba：应探测出trigram（contrib自动启用）
	mode, _ := meta.SearchMode()
	require.Equal(t, "trigram", mode, "应自动探测出pg_trgm")

	ctx := context.Background()
	uid := func() interface{} {
		reply := executor.run(ctx, `mutation { createUser(input: { name: "作者", email: "z@x.com" }) { id } }`, nil, "")
		require.Empty(t, reply.Errors, "%v", reply.Errors)
		return reply.Data["createUser"].(map[string]interface{})["id"]
	}()
	for _, p := range []map[string]interface{}{
		{"title": "PostgreSQL数据库引擎选型", "content": "全文检索方案对比"},
		{"title": "Go语言实践", "content": "数据库连接池调优"},
		{"title": "前端构建", "content": "与后端无关"},
	} {
		p["userId"] = uid
		reply := executor.run(ctx, `mutation ($in: PostCreateInput!) { createPost(input: $in) { id } }`,
			map[string]interface{}{"in": p}, "")
		require.Empty(t, reply.Errors, "%v", reply.Errors)
	}

	// 中文搜索：标题或内容命中"数据库"的两篇命中、无关的一篇排除
	// （trigram的similarity是整串相似度，命中顺序不做强断言）
	reply := executor.run(ctx, `query { posts(search: "数据库") { items { title } total } }`, nil, "")
	require.Empty(t, reply.Errors, "搜索失败: %v", reply.Errors)
	posts := reply.Data["posts"].(map[string]interface{})
	require.EqualValues(t, 2, posts["total"])
	titles := map[string]bool{}
	for _, item := range posts["items"].([]interface{}) {
		titles[item.(map[string]interface{})["title"].(string)] = true
	}
	require.True(t, titles["PostgreSQL数据库引擎选型"] && titles["Go语言实践"], "中文子串命中应包含标题与内容两种来源: %v", titles)

	// 搜索+条件组合
	reply = executor.run(ctx, `query { posts(search: "数据库", where: { title: { like: "%Go%" } }) { items { title } } }`, nil, "")
	require.Empty(t, reply.Errors)
	items := reply.Data["posts"].(map[string]interface{})["items"].([]interface{})
	require.Len(t, items, 1)
	require.Equal(t, "Go语言实践", items[0].(map[string]interface{})["title"])
}

// TestExecutorFragments fragment展开：命名/嵌套/内联fragment正确编译进SQL
func TestExecutorFragments(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	ctx := context.Background()

	reply := executor.run(ctx, `mutation { createUser(input: { name: "F", email: "f@x.com" }) { id } }`, nil, "")
	require.Empty(t, reply.Errors, "准备数据失败: %v", reply.Errors)

	reply = executor.run(ctx, `
		fragment UserCore on User { id name }
		fragment UserFull on User { ...UserCore email }
		query {
			users {
				items { ...UserFull }
				... on UserResult { total }
			}
		}
	`, nil, "")
	require.Empty(t, reply.Errors, "fragment查询失败: %v", reply.Errors)

	users := reply.Data["users"].(map[string]interface{})
	require.EqualValues(t, 1, users["total"], "内联fragment中的total应生效")
	item := users["items"].([]interface{})[0].(map[string]interface{})
	require.Equal(t, "F", item["name"], "嵌套fragment字段应生效")
	require.Equal(t, "f@x.com", item["email"])
	require.NotNil(t, item["id"])
}

// TestExecutorRelationOps 嵌套写入真库验证：创建挂接、多对多connect/disconnect原子完成
func TestExecutorRelationOps(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	ctx := context.Background()

	run := func(query string, vars map[string]interface{}) map[string]interface{} {
		reply := executor.run(ctx, query, vars, "")
		require.Empty(t, reply.Errors, "执行失败: %v", reply.Errors)
		return reply.Data
	}

	// 既有数据：作者 + 两篇游离文章 + 两个标签
	author := run(`mutation { createUser(input: { name: "Au", email: "au@x.com" }) { id } }`, nil)["createUser"].(map[string]interface{})["id"]
	orphanA := run(`mutation ($u: ID!) { createPost(input: { title: "PA", userId: $u }) { id } }`,
		map[string]interface{}{"u": author})["createPost"].(map[string]interface{})["id"]
	_ = run(`mutation ($u: ID!) { createPost(input: { title: "PB", userId: $u }) { id } }`, map[string]interface{}{"u": author})
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
		reply := executor.run(ctx, `mutation ($n: String!, $e: String!) {
			createUser(input: { name: $n, email: $e }) { id }
		}`, map[string]interface{}{"n": name, "e": name + "@x.com"}, "")
		require.Empty(t, reply.Errors, "准备数据失败: %v", reply.Errors)
	}

	// 向前遍历：每页2条，应得 [A,B] [C,D] [E]
	var pages [][]string
	cursor := interface{}(nil)
	for i := 0; i < 5; i++ { // 上限防死循环
		reply := executor.run(ctx, `query ($c: Cursor) {
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
	reply := executor.run(ctx, `query {
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

	// 页大小用变量：同一查询文本不同$n（一份计划适配任意页大小）
	for _, n := range []int{2, 3} {
		reply = executor.run(ctx, `query ($n: Int) {
			users(first: $n, sort: { name: ASC }) { items { name } pageInfo { hasNext } }
		}`, map[string]interface{}{"n": n}, "")
		require.Empty(t, reply.Errors, "变量页大小失败: %v", reply.Errors)
		users = reply.Data["users"].(map[string]interface{})
		require.Len(t, users["items"].([]interface{}), n, "first=$n 应返回n条")
		require.Equal(t, true, users["pageInfo"].(map[string]interface{})["hasNext"], "5条数据取前%d应有下一页", n)
	}
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
		reply := executor.run(ctx, m, nil, "")
		require.Empty(t, reply.Errors, "准备数据失败: %v", reply.Errors)
	}

	// 全表聚合
	reply := executor.run(ctx, `query { userStats { count email { countDistinct } } }`, nil, "")
	require.Empty(t, reply.Errors, "全表聚合失败: %v", reply.Errors)
	rows := reply.Data["userStats"].([]interface{})
	require.Len(t, rows, 1)
	row := rows[0].(map[string]interface{})
	require.EqualValues(t, 3, row["count"])
	require.EqualValues(t, 3, row["email"].(map[string]interface{})["countDistinct"])

	// 分组聚合
	reply = executor.run(ctx, `query { userStats(groupBy: ["name"], where: { name: { eq: "B" } }) { key count } }`, nil, "")
	require.Empty(t, reply.Errors, "分组聚合失败: %v", reply.Errors)
	rows = reply.Data["userStats"].([]interface{})
	require.Len(t, rows, 1)
	row = rows[0].(map[string]interface{})
	require.EqualValues(t, 2, row["count"])
	require.Equal(t, "B", row["key"].(map[string]interface{})["name"])

	// having：分组后按聚合值过滤（库内HAVING，A组count=1被滤，只剩B组count=2）
	reply = executor.run(ctx, `query { userStats(groupBy: ["name"], having: { count: { gt: 1 } }) { key count } }`, nil, "")
	require.Empty(t, reply.Errors, "having过滤失败: %v", reply.Errors)
	rows = reply.Data["userStats"].([]interface{})
	require.Len(t, rows, 1, "having count>1 只应返回B组")
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
	reply := executor.runOperation(ctx, "AddUser", map[string]interface{}{
		"input": map[string]interface{}{"name": "Carol", "email": "c@x.com"},
	})
	require.Empty(t, reply.Errors, "执行持久化变更失败: %v", reply.Errors)
	require.Equal(t, "Carol", reply.Data["createUser"].(map[string]interface{})["name"])

	reply = executor.runOperation(ctx, "ListUsers", map[string]interface{}{"name": "Carol"})
	require.Empty(t, reply.Errors, "执行持久化查询失败: %v", reply.Errors)
	require.EqualValues(t, 1, reply.Data["users"].(map[string]interface{})["total"])

	// 未知操作名报错
	reply = executor.runOperation(ctx, "Nope", nil)
	require.NotEmpty(t, reply.Errors)
	require.Contains(t, reply.Errors[0].Message, "未找到")
}

// TestExecutorScope 行级作用域真库验证：WithScope 注入租户，查询只返回该租户的行，
// 对客户端透明（schema 不变）。无作用域上下文则匹配不到行（安全默认）
func TestExecutorScope(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	require.NoError(t, db.Exec(`ALTER TABLE users ADD COLUMN tenant_id INT NOT NULL DEFAULT 1`).Error)

	k, err := std.NewKonfig()
	require.NoError(t, err)
	k.Set("mode", "dev")
	k.Set("app.root", t.TempDir())
	k.Set("schema.schema", "public")
	k.Set("metadata.classes", map[string]*internal.ClassConfig{
		"User": {Table: "users", Scope: []internal.ScopeConfig{{Column: "tenant_id", Context: "tenant"}}},
	})
	meta, err := NewMetadata(k, db)
	require.NoError(t, err, "加载元数据失败")
	compile, err := NewCompiler(meta, nil)
	require.NoError(t, err)
	executor, err := NewExecutor(db, NewRenderer(meta), meta, compile)
	require.NoError(t, err)

	require.NoError(t, db.Exec(`INSERT INTO users (name, email, tenant_id) VALUES
		('t1a','t1a@x.com',1), ('t1b','t1b@x.com',1), ('t2a','t2a@x.com',2)`).Error)

	// 租户1上下文：只看到自己的2条
	ctx := WithScope(context.Background(), map[string]any{"tenant": 1})
	reply := executor.run(ctx, `{ users { items { name } total } }`, nil, "")
	require.Empty(t, reply.Errors, "租户1查询失败: %v", reply.Errors)
	require.EqualValues(t, 2, reply.Data["users"].(map[string]interface{})["total"], "租户1应只看到2条")

	// 租户2：只看到1条
	ctx = WithScope(context.Background(), map[string]any{"tenant": 2})
	reply = executor.run(ctx, `{ users { total } }`, nil, "")
	require.Empty(t, reply.Errors, "租户2查询失败: %v", reply.Errors)
	require.EqualValues(t, 1, reply.Data["users"].(map[string]interface{})["total"], "租户2应只看到1条")

	// 客户端想偷看别的租户：where 叠加只会更窄，绕不过强制作用域
	reply = executor.run(ctx, `{ users(where: { name: { eq: "t1a" } }) { total } }`, nil, "")
	require.Empty(t, reply.Errors)
	require.EqualValues(t, 0, reply.Data["users"].(map[string]interface{})["total"], "租户2看不到租户1的行")

	// 无作用域上下文：tenant_id = NULL，匹配不到任何行（安全默认）
	reply = executor.run(context.Background(), `{ users { total } }`, nil, "")
	require.Empty(t, reply.Errors)
	require.EqualValues(t, 0, reply.Data["users"].(map[string]interface{})["total"], "无作用域应查不到行")

	// 写隔离：create 自动填租户列（即使不传/传错），新行落在当前租户
	ctx1 := WithScope(context.Background(), map[string]any{"tenant": 1})
	reply = executor.run(ctx1, `mutation { createUser(input: { name: "new1", email: "new1@x.com" }) { id } }`, nil, "")
	require.Empty(t, reply.Errors, "租户1创建失败: %v", reply.Errors)
	newID := reply.Data["createUser"].(map[string]interface{})["id"]
	var tid int
	require.NoError(t, db.Raw(`SELECT tenant_id FROM users WHERE id = ?`, newID).Scan(&tid).Error)
	require.Equal(t, 1, tid, "create 应自动填当前租户")

	// 写隔离：update/delete 只能改本租户的行——租户2 改不到租户1 的 new1
	ctx2 := WithScope(context.Background(), map[string]any{"tenant": 2})
	reply = executor.run(ctx2, `mutation { updateUser(input: { name: "hacked" }, where: { name: { eq: "new1" } }) { id } }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	require.Empty(t, reply.Data["updateUser"], "租户2 不应改到租户1 的行")
	var name string
	require.NoError(t, db.Raw(`SELECT name FROM users WHERE id = ?`, newID).Scan(&name).Error)
	require.Equal(t, "new1", name, "租户1 的行未被租户2 篡改")

	// 租户1 自己能改
	reply = executor.run(ctx1, `mutation { updateUser(input: { name: "own" }, where: { name: { eq: "new1" } }) { id name } }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	require.Equal(t, "own", reply.Data["updateUser"].(map[string]interface{})["name"], "租户1 改自己的行成功")

	// 安全回归：客户端整体变量 input 偷传作用域列 → 编译器层硬拒绝（堵整体变量绕过 schema）
	reply = executor.run(ctx1, `mutation ($i: UserUpdateInput!) { updateUser(input: $i, where: { name: { eq: "own" } }) { id } }`,
		map[string]any{"i": map[string]any{"name": "z", "tenantId": 2}}, "")
	require.NotEmpty(t, reply.Errors, "偷传作用域列应被拒绝")
	require.Contains(t, reply.Errors[0].Message, "作用域列")
	require.NoError(t, db.Raw(`SELECT name FROM users WHERE id = ?`, newID).Scan(&name).Error)
	require.Equal(t, "own", name, "被拒绝后行未被篡改")

	// 安全回归：别租户用本租户行的唯一键 upsert → DO UPDATE 被作用域 WHERE 阻止，不劫持
	reply = executor.run(ctx2, `mutation { upsertUsers(input: [{ name: "hijacked", email: "new1@x.com" }], on: ["email"]) { id } }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	var hijTenant int
	require.NoError(t, db.Raw(`SELECT name, tenant_id FROM users WHERE email = 'new1@x.com'`).Row().Scan(&name, &hijTenant))
	require.Equal(t, "own", name, "租户2 不应劫持租户1 的行（DO UPDATE 被作用域 WHERE 阻止）")
	require.Equal(t, 1, hijTenant, "租户1 行的 tenant_id 未被改写")

	// 统计聚合也隔离：count 应与查询 total 一致（都只算本租户），不泄露全表
	reply = executor.run(ctx1, `query { userStats { count } users { total } }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	statsCount := reply.Data["userStats"].([]interface{})[0].(map[string]interface{})["count"]
	queryTotal := reply.Data["users"].(map[string]interface{})["total"]
	require.NotZero(t, statsCount, "租户1 应有数据")
	require.EqualValues(t, queryTotal, statsCount, "统计聚合应与查询同样按作用域隔离，不泄露全表")
}

// TestExecutorScopeRelation 关系挂接的作用域隔离真库验证：
// m2m connect 经 SELECT...AND scope 校验，不能挂接别租户的目标行（防跨租户关联）
func TestExecutorScopeRelation(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	require.NoError(t, db.Exec(`ALTER TABLE posts ADD COLUMN tenant_id INT NOT NULL DEFAULT 1`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE tags ADD COLUMN tenant_id INT NOT NULL DEFAULT 1`).Error)

	k, err := std.NewKonfig()
	require.NoError(t, err)
	k.Set("mode", "dev")
	k.Set("app.root", t.TempDir())
	k.Set("schema.schema", "public")
	k.Set("metadata.classes", map[string]*internal.ClassConfig{
		"Post": {Table: "posts", Scope: []internal.ScopeConfig{{Column: "tenant_id", Context: "tenant"}}},
		"Tag":  {Table: "tags", Scope: []internal.ScopeConfig{{Column: "tenant_id", Context: "tenant"}}},
	})
	meta, err := NewMetadata(k, db)
	require.NoError(t, err)
	compile, err := NewCompiler(meta, nil)
	require.NoError(t, err)
	executor, err := NewExecutor(db, NewRenderer(meta), meta, compile)
	require.NoError(t, err)

	// 租户1 的 post 与 tag1，租户2 的 tag2
	require.NoError(t, db.Exec(`INSERT INTO users (id, name, email) VALUES (1, 'u', 'u@x.com')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO posts (id, title, user_id, tenant_id) VALUES (1, 'p1', 1, 1)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO tags (id, name, tenant_id) VALUES (1, 't1', 1), (2, 't2', 2)`).Error)

	ctx1 := WithScope(context.Background(), map[string]any{"tenant": 1})
	count := func(tagID int) int {
		var c int
		require.NoError(t, db.Raw(`SELECT COUNT(*) FROM post_tags WHERE post_id=1 AND tag_id=?`, tagID).Scan(&c).Error)
		return c
	}

	// 租户1 想挂接租户2 的 tag2 → 校验后不建立关联
	reply := executor.run(ctx1, `mutation { updatePost(input: { tags: { connect: [2] } }, id: 1) { id } }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	require.Equal(t, 0, count(2), "不应建立到别租户 tag 的关联（跨租户挂接被阻止）")

	// 租户1 挂接本租户 tag1 → 成功
	reply = executor.run(ctx1, `mutation { updatePost(input: { tags: { connect: [1] } }, id: 1) { id } }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	require.Equal(t, 1, count(1), "应能挂接本租户 tag")
}
