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
		reply = executor.Execute(ctx, `mutation ($t: String!, $u: Int!) {
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
