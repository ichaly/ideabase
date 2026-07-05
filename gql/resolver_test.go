package gql

import (
	"context"
	"fmt"
	"testing"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/compiler/pgsql"
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
)

// TestHostsNestedArrays 宿主拍平：连续两层数组段（一对多里嵌一对多）须收齐全部叶子宿主。
// 回归——单元素cur首段侥幸安全，多元素cur上原地复用底层数组会覆写未读元素
func TestHostsNestedArrays(t *testing.T) {
	const width = 8
	items := make([]interface{}, width)
	for i := 0; i < width; i++ {
		children := make([]interface{}, width)
		for j := 0; j < width; j++ {
			children[j] = map[string]interface{}{"id": i*width + j}
		}
		items[i] = map[string]interface{}{"children": children}
	}
	root := map[string]interface{}{"items": items}

	got := hosts(root, []string{"items", "children"})
	require.Len(t, got, width*width, "两层数组段应收齐全部叶子宿主")

	seen := make(map[int]bool, len(got))
	for _, host := range got {
		seen[host["id"].(int)] = true
	}
	require.Len(t, seen, width*width, "宿主不得重复或丢失（原地复用会覆写）")
}

// greetResolver 单对象解析器：拼接问候语
type greetResolver struct{}

func (my *greetResolver) Name() string { return "greet" }
func (my *greetResolver) Resolve(_ context.Context, source map[string]interface{}, _ map[string]interface{}) (interface{}, error) {
	return fmt.Sprintf("Hello, %v!", source["name"]), nil
}

// labelResolver 批量解析器：记录调用次数验证免N+1
type labelResolver struct{ calls int }

func (my *labelResolver) Name() string { return "label" }
func (my *labelResolver) Resolve(_ context.Context, source map[string]interface{}, _ map[string]interface{}) (interface{}, error) {
	return nil, fmt.Errorf("批量解析器不应走单对象路径")
}
func (my *labelResolver) ResolveBatch(_ context.Context, sources []map[string]interface{}, _ map[string]interface{}) ([]interface{}, error) {
	my.calls++
	values := make([]interface{}, len(sources))
	for i, source := range sources {
		values[i] = fmt.Sprintf("#%v", source["id"])
	}
	return values, nil
}

// TestResolver 自定义resolver端到端：虚拟字段不进SQL，执行后填充，批量免N+1
func TestResolver(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	k, err := std.NewKonfig()
	require.NoError(t, err, "创建配置失败")
	k.Set("mode", "dev")
	k.Set("app.root", t.TempDir())
	k.Set("schema.schema", "public")
	// 配置两个resolver虚拟字段
	k.Set("metadata.classes", map[string]*internal.ClassConfig{
		"User": {
			Table: "users",
			Fields: map[string]*internal.FieldConfig{
				"greeting": {Type: "String", IsNullable: true, Resolver: "greet"},
			},
		},
		"Post": {
			Table: "posts",
			Fields: map[string]*internal.FieldConfig{
				"label": {Type: "String", IsNullable: true, Resolver: "label"},
			},
		},
	})

	meta, err := NewMetadata(k, db)
	require.NoError(t, err, "加载元数据失败")

	compile, err := NewCompiler(meta, []compiler.Dialect{pgsql.NewDialect()})
	require.NoError(t, err, "创建编译器失败")

	executor, err := NewExecutor(db, NewRenderer(meta), meta, compile)
	require.NoError(t, err, "创建执行器失败")

	label := &labelResolver{}
	executor.Register(&greetResolver{}, label)

	ctx := context.Background()

	// 准备数据：1个用户2篇文章（变更读回也应执行resolver）
	reply := executor.Execute(ctx, `mutation {
		createUser(input: { name: "Alice", email: "alice@x.com" }) { id name greeting }
	}`, nil, "")
	require.Empty(t, reply.Errors, "创建用户失败: %v", reply.Errors)
	created := reply.Data["createUser"].(map[string]interface{})
	require.Equal(t, "Hello, Alice!", created["greeting"])
	userId := created["id"]

	for _, title := range []string{"A", "B"} {
		reply = executor.Execute(ctx, `mutation ($t: String!, $u: ID!) {
			createPost(input: { title: $t, userId: $u }) { id }
		}`, map[string]interface{}{"t": title, "u": userId}, "")
		require.Empty(t, reply.Errors, "创建文章失败: %v", reply.Errors)
	}

	// 嵌套列表上的批量resolver：2个宿主对象一次调用
	reply = executor.Execute(ctx, `query {
		users { items { id name greeting posts { id label } } }
	}`, nil, "")
	require.Empty(t, reply.Errors, "查询失败: %v", reply.Errors)

	items := reply.Data["users"].(map[string]interface{})["items"].([]interface{})
	require.Len(t, items, 1)
	user := items[0].(map[string]interface{})
	require.Equal(t, "Hello, Alice!", user["greeting"])

	posts := user["posts"].([]interface{})
	require.Len(t, posts, 2)
	for _, p := range posts {
		post := p.(map[string]interface{})
		require.Equal(t, fmt.Sprintf("#%v", post["id"]), post["label"])
	}
	require.Equal(t, 1, label.calls, "批量resolver应一次调用处理整个列表（免N+1）")

	// 未注册的resolver报错
	delete(executor.resolvers, "greet")
	reply = executor.Execute(ctx, `query { users { items { id greeting } } }`, nil, "")
	require.NotEmpty(t, reply.Errors, "未注册resolver应报错")
	require.Contains(t, reply.Errors[0].Message, "resolver未注册")
}

// TestResolverResultSuffixClass 本名以Result结尾的实体（如ExamResult）：
// 回归——根字段类型名被无条件剪Result后缀（ExamResult→Exam），
// 导致该子树的resolver绑定静默丢失
func TestResolverResultSuffixClass(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, func(k *std.Konfig) {
		k.Set("metadata.classes", map[string]*internal.ClassConfig{
			"ExamResult": {
				Table: "posts",
				Fields: map[string]*internal.FieldConfig{
					"greeting": {Type: "String", IsNullable: true, Resolver: "greet"},
				},
			},
		})
	})
	defer cleanup()
	executor.Register(&greetResolver{})
	ctx := context.Background()

	reply := executor.Execute(ctx, `mutation { createUser(input: { name: "E", email: "e@x.com" }) { id } }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	uid := reply.Data["createUser"].(map[string]interface{})["id"]

	// 变更读回：根字段类型名即实体本名ExamResult，按原名命中，不得剪成Exam
	reply = executor.Execute(ctx, `mutation ($u: ID!) {
		createExamResult(input: { title: "T", userId: $u }) { id title greeting }
	}`, map[string]interface{}{"u": uid}, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	created := reply.Data["createExamResult"].(map[string]interface{})
	require.NotNil(t, created["greeting"], "变更读回应执行resolver（Result后缀不得误剪）")

	// 查询根：包装类型ExamResultResult原名miss后剪一次后缀应命中
	reply = executor.Execute(ctx, `query { examResults { items { id greeting } } }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	items := reply.Data["examResults"].(map[string]interface{})["items"].([]interface{})
	require.NotEmpty(t, items)
	require.NotNil(t, items[0].(map[string]interface{})["greeting"], "查询根resolver绑定应生效")
}
