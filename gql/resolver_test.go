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
	"gorm.io/gorm"
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

// greetResolver 单对象解析器（注册即声明）：拼接问候语，class参数化供不同宿主实体复用
func greetResolver(class string) Resolver {
	return NewResolver(class, "greeting", "问候语",
		func(_ context.Context, source Source, _ struct{}) (string, error) {
			return fmt.Sprintf("Hello, %v!", source["name"]), nil
		})
}

// labelResolver 批量解析器（注册即声明）：记录调用次数验证免N+1
func labelResolver(calls *int) Resolver {
	return NewBatch("Post", "label", "标签",
		func(_ context.Context, sources []Source, _ struct{}) ([]string, error) {
			*calls++
			values := make([]string, len(sources))
			for i, source := range sources {
				values[i] = fmt.Sprintf("#%v", source["id"])
			}
			return values, nil
		})
}

// TestResolver 自定义resolver端到端：注册即声明挂载虚拟字段，
// 不进SQL、执行后填充，批量免N+1
func TestResolver(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	k, err := std.NewKonfig()
	require.NoError(t, err, "创建配置失败")
	k.Set("mode", "dev")
	k.Set("app.root", t.TempDir())
	k.Set("schema.schema", "public")
	k.Set("metadata.classes", map[string]*internal.ClassConfig{
		"User": {Table: "users"},
		"Post": {Table: "posts"},
	})

	meta, err := NewMetadata(k, db)
	require.NoError(t, err, "加载元数据失败")

	compile, err := NewCompiler(meta, []compiler.Dialect{pgsql.NewDialect()})
	require.NoError(t, err, "创建编译器失败")

	executor, err := NewExecutor(db, NewRenderer(meta), meta, compile)
	require.NoError(t, err, "创建执行器失败")

	var calls int
	require.NoError(t, executor.Register(greetResolver("User"), labelResolver(&calls)))

	ctx := context.Background()

	// 准备数据：1个用户2篇文章（变更读回也应执行resolver）
	reply := executor.run(ctx, `mutation {
		createUser(input: { name: "Alice", email: "alice@x.com" }) { id name greeting }
	}`, nil, "")
	require.Empty(t, reply.Errors, "创建用户失败: %v", reply.Errors)
	created := reply.Data["createUser"].(map[string]interface{})
	require.Equal(t, "Hello, Alice!", created["greeting"])
	userId := created["id"]

	for _, title := range []string{"A", "B"} {
		reply = executor.run(ctx, `mutation ($t: String!, $u: ID!) {
			createPost(input: { title: $t, userId: $u }) { id }
		}`, map[string]interface{}{"t": title, "u": userId}, "")
		require.Empty(t, reply.Errors, "创建文章失败: %v", reply.Errors)
	}

	// 嵌套列表上的批量resolver：2个宿主对象一次调用
	reply = executor.run(ctx, `query {
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
	require.Equal(t, 1, calls, "批量resolver应一次调用处理整个列表（免N+1）")

	// 未注册的resolver报错
	delete(executor.resolvers, "User.greeting")
	reply = executor.run(ctx, `query { users { items { id greeting } } }`, nil, "")
	require.NotEmpty(t, reply.Errors, "未注册resolver应报错")
	require.Contains(t, reply.Errors[0].Message, "resolver未注册")
}

// TestResolverArgs resolver字段参数（注册即声明新增能力）：
// schema渲染参数签名，执行期按请求变量解出实参并解码进强类型struct、validate校验
func TestResolverArgs(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	require.NoError(t, executor.Register(NewResolver("User", "hello", "问候",
		func(_ context.Context, source Source, args struct {
			Lang string `json:"lang" validate:"omitempty,oneof=zh en"`
		}) (string, error) {
			if args.Lang == "zh" {
				return fmt.Sprintf("你好, %v!", source["name"]), nil
			}
			return fmt.Sprintf("Hello, %v!", source["name"]), nil
		})))
	ctx := context.Background()

	reply := executor.run(ctx, `mutation { createUser(input: { name: "Ann", email: "a@x.com" }) { id } }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)

	// 字面量实参
	reply = executor.run(ctx, `query { users { items { name hello(lang: "zh") } } }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	items := reply.Data["users"].(map[string]interface{})["items"].([]interface{})
	require.Equal(t, "你好, Ann!", items[0].(map[string]interface{})["hello"])

	// 变量实参：连跑两次覆盖计划缓存命中路径的实参解析（绑定AST随计划复用）
	for i := 0; i < 2; i++ {
		reply = executor.run(ctx, `query ($l: String) { users { items { name hello(lang: $l) } } }`,
			map[string]interface{}{"l": "en"}, "")
		require.Empty(t, reply.Errors, "%v", reply.Errors)
		items = reply.Data["users"].(map[string]interface{})["items"].([]interface{})
		require.Equal(t, "Hello, Ann!", items[0].(map[string]interface{})["hello"])
	}

	// validate校验：非法枚举值报错
	reply = executor.run(ctx, `query { users { items { name hello(lang: "xx") } } }`, nil, "")
	require.NotEmpty(t, reply.Errors, "validate应拦截非法参数")
}

// TestResolverResultSuffixClass 本名以Result结尾的实体（如ExamResult）：
// 回归——根字段类型名被无条件剪Result后缀（ExamResult→Exam），
// 导致该子树的resolver绑定静默丢失
func TestResolverResultSuffixClass(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, func(k *std.Konfig) {
		k.Set("metadata.classes", map[string]*internal.ClassConfig{
			"ExamResult": {Table: "posts"},
		})
	})
	defer cleanup()
	require.NoError(t, executor.Register(greetResolver("ExamResult")))
	ctx := context.Background()

	reply := executor.run(ctx, `mutation { createUser(input: { name: "E", email: "e@x.com" }) { id } }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	uid := reply.Data["createUser"].(map[string]interface{})["id"]

	// 变更读回：根字段类型名即实体本名ExamResult，按原名命中，不得剪成Exam
	reply = executor.run(ctx, `mutation ($u: ID!) {
		createExamResult(input: { title: "T", userId: $u }) { id title greeting }
	}`, map[string]interface{}{"u": uid}, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	created := reply.Data["createExamResult"].(map[string]interface{})
	require.NotNil(t, created["greeting"], "变更读回应执行resolver（Result后缀不得误剪）")

	// 查询根：包装类型ExamResultResult原名miss后剪一次后缀应命中
	reply = executor.run(ctx, `query { examResults { items { id greeting } } }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	items := reply.Data["examResults"].(map[string]interface{})["items"].([]interface{})
	require.NotEmpty(t, items)
	require.NotNil(t, items[0].(map[string]interface{})["greeting"], "查询根resolver绑定应生效")
}

// TestFieldResolverRoots 同一个泛型字段Resolver抽象应同时覆盖Query/Mutation根字段；
// Root为零大小强类型source，业务实现不接触map/any。
func TestFieldResolverRoots(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()

	type pingArgs struct {
		Message string `json:"message" validate:"required"`
	}
	ping := NewResolver("Query", "ping", "探活",
		func(_ context.Context, _ Root, args pingArgs) (string, error) {
			return "pong:" + args.Message, nil
		})
	require.NoError(t, executor.Register(ping))

	reply := executor.run(context.Background(), `query { ping(message: "hi") users { total } }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	require.Equal(t, "pong:hi", reply.Data["ping"])
	require.Equal(t, int64(0), reply.Data["users"].(map[string]any)["total"])
}

// TestFieldResolverEntity 泛型字段Resolver与根Resolver使用同一个构造器，实体字段
// 仍可直接使用零转换的Source热路径。
func TestFieldResolverEntity(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()

	resolver := NewResolver("User", "displayName", "显示名",
		func(_ context.Context, source Source, _ struct{}) (string, error) {
			return "@" + source["name"].(string), nil
		})
	require.NoError(t, executor.Register(resolver))

	reply := executor.run(context.Background(), `mutation {
		createUser(input: { name: "Alice", email: "alice@x.com" }) { name displayName }
	}`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	require.Equal(t, "@Alice", reply.Data["createUser"].(map[string]any)["displayName"])
}

// TestFieldResolverReplaceGenerated 显式Replace可覆盖引擎按表生成的默认CRUD字段，
// 但普通Register不得静默覆盖同一coordinate。
func TestFieldResolverReplaceGenerated(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()

	type args struct {
		Input map[string]any `json:"input"`
	}
	override := NewResolver("Mutation", "createUser", "",
		func(_ context.Context, _ Root, input args) (map[string]any, error) {
			return map[string]any{"id": int64(99), "name": "custom:" + input.Input["name"].(string)}, nil
		}, Existing())

	require.Error(t, executor.Register(override), "已有schema字段必须显式Replace")
	require.NoError(t, executor.Replace(override))

	reply := executor.run(context.Background(), `mutation {
		createUser(input: { name: "Alice", email: "alice@x.com" }) { id name }
	}`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	require.Equal(t, "custom:Alice", reply.Data["createUser"].(map[string]any)["name"])
}

func TestFieldResolverWrap(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()

	type args struct {
		Message string `json:"message"`
	}
	base := NewResolver("Query", "echo", "",
		func(_ context.Context, _ Root, input args) (string, error) {
			return input.Message, nil
		})
	require.NoError(t, executor.Register(base))
	require.NoError(t, executor.Wrap("Query.echo",
		NewResolverMiddleware(func(ctx context.Context, source Root, input args, next NextResolver[Root, args, string]) (string, error) {
			input.Message = "wrapped:" + input.Message
			return next(ctx, source, input)
		})))

	reply := executor.run(context.Background(), `{ echo(message: "ok") }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	require.Equal(t, "wrapped:ok", reply.Data["echo"])
}

func pingResolver() Resolver {
	type args struct {
		Msg string `json:"msg" validate:"required"`
	}
	return NewResolver("Query", "ping", "", func(_ context.Context, _ Root, input args) (string, error) {
		return "pong:" + input.Msg, nil
	})
}

// signUpReq 参数即声明：json定名，validate含required渲染为非空!
type signUpReq struct {
	Name  string `json:"name" validate:"required"`
	Email string `json:"email" validate:"required"`
}

// newSignUpResolver 编排型根Resolver：建号返回id，引擎按选择集回查补全；
// 返回类型int64本推导为Int，Result选项覆盖为实体User触发回查
func newSignUpResolver(db *gorm.DB) Resolver {
	return NewResolver("Mutation", "signUp", "建号并回查主档", func(ctx context.Context, _ Root, req signUpReq) (int64, error) {
		var id int64
		err := db.WithContext(ctx).
			Raw("INSERT INTO users(name, email) VALUES(?, ?) RETURNING id", req.Name, req.Email).
			Scan(&id).Error
		return id, err
	}, Result("User"))
}

// setupRootResolverExecutor 与setupTestExecutor同构，但透出db供Resolver闭包使用
func setupRootResolverExecutor(t *testing.T) (*Executor, *gorm.DB, func()) {
	return newTestExecutor(t, nil)
}

// TestRootResolverRoundTrip 根Resolver端到端：注册→内省可见→分发执行→回查补全→默认字段混排
func TestRootResolverRoundTrip(t *testing.T) {
	executor, db, cleanup := setupRootResolverExecutor(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, executor.Register(pingResolver(), newSignUpResolver(db)))

	// 1. 内省可见：自定义字段与表CRUD一视同仁
	reply := executor.run(ctx, `{ __type(name: "Mutation") { fields { name } } }`, nil, "")
	require.Empty(t, reply.Errors, "自省失败: %v", reply.Errors)
	fields := reply.Data["__type"].(map[string]interface{})["fields"].([]interface{})
	var names []string
	for _, f := range fields {
		names = append(names, f.(map[string]interface{})["name"].(string))
	}
	require.Contains(t, names, "signUp", "Resolver应出现在Mutation自省结果中")

	// 2. 标量直通：结果原样输出（连跑两次覆盖计划缓存命中路径）
	for i := 0; i < 2; i++ {
		reply = executor.run(ctx, `query { ping(msg: "hi") }`, nil, "")
		require.Empty(t, reply.Errors, "ping执行失败: %v", reply.Errors)
		require.Equal(t, "pong:hi", reply.Data["ping"])
	}

	// 3. 回查补全：Resolver返回id，引擎按客户端选择集（含别名与关系）读回实体
	reply = executor.run(ctx, `mutation ($n: String!, $e: String!) {
		u: signUp(name: $n, email: $e) { id name posts { title } }
	}`, map[string]interface{}{"n": "Alice", "e": "alice@x.com"}, "")
	require.Empty(t, reply.Errors, "signUp执行失败: %v", reply.Errors)
	user := reply.Data["u"].(map[string]interface{})
	require.Equal(t, "Alice", user["name"])
	require.NotNil(t, user["id"])
	require.NotNil(t, user["posts"], "关系字段应随回查补全")

	// 4. 变量校验：未注册字段仍被schema校验拦截
	reply = executor.run(ctx, `mutation { nosuch(x: 1) }`, nil, "")
	require.NotEmpty(t, reply.Errors, "未定义字段应报错")

	// 5. 自定义根Resolver与默认数据库字段可混排
	reply = executor.run(ctx, `query { ping(msg: "x") users { total } }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	require.Equal(t, "pong:x", reply.Data["ping"])
	require.NotNil(t, reply.Data["users"])
}

// TestRootResolverTypename __typename与Resolver共存（Apollo客户端默认注入）。
func TestRootResolverTypename(t *testing.T) {
	executor, db, cleanup := setupRootResolverExecutor(t)
	defer cleanup()
	ctx := context.Background()
	require.NoError(t, executor.Register(pingResolver(), newSignUpResolver(db)))

	// 查询操作回填"Query"
	reply := executor.run(ctx, `query { ping(msg: "hi") __typename }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	require.Equal(t, "pong:hi", reply.Data["ping"])
	require.Equal(t, "Query", reply.Data["__typename"])

	// 变更操作回填"Mutation"（含别名）
	reply = executor.run(ctx, `mutation ($n: String!, $e: String!) {
		signUp(name: $n, email: $e) { id } t: __typename
	}`, map[string]interface{}{"n": "Ty", "e": "ty@x.com"}, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	require.Equal(t, "Mutation", reply.Data["t"])
}

// TestRootResolverVariablePassthrough 回查合成查询透传原变量声明：
// 回归——对象/枚举变量经json内联会产生带引号键、带引号枚举的非法GraphQL字面量，
// 导致enrich合成回查解析失败
func TestRootResolverVariablePassthrough(t *testing.T) {
	executor, db, cleanup := setupRootResolverExecutor(t)
	defer cleanup()
	ctx := context.Background()
	require.NoError(t, executor.Register(newSignUpResolver(db)))

	// 准备：建号并为其创建两篇文章（回查嵌套选择集有数据可过滤/排序）
	reply := executor.run(ctx, `mutation ($n: String!, $e: String!) {
		signUp(name: $n, email: $e) { id }
	}`, map[string]interface{}{"n": "Vera", "e": "vera@x.com"}, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	uid := reply.Data["signUp"].(map[string]interface{})["id"]
	for _, title := range []string{"B2", "A1"} {
		reply = executor.run(ctx, `mutation ($t: String!, $u: ID!) {
			createPost(input: { title: $t, userId: $u }) { id }
		}`, map[string]interface{}{"t": title, "u": uid}, "")
		require.Empty(t, reply.Errors, "%v", reply.Errors)
	}

	// 嵌套选择集引用对象+枚举变量（$s为[{title: ASC}]，json内联会产生带引号键
	// 与带引号枚举的非法字面量）与字段级标量变量（$lk），连跑两次覆盖回查计划缓存
	for i := 0; i < 2; i++ {
		reply = executor.run(ctx, `mutation ($n: String!, $e: String!, $lk: String, $s: [PostSortInput!]) {
			signUp(name: $n, email: $e) { id name posts(where: { title: { like: $lk } }, sort: $s) { title } }
		}`, map[string]interface{}{
			"n": "Bob", "e": fmt.Sprintf("bob%d@x.com", i),
			"lk": "%",
			"s":  []interface{}{map[string]interface{}{"title": "ASC"}},
		}, "")
		require.Empty(t, reply.Errors, "对象/枚举变量应透传而非内联: %v", reply.Errors)
		user := reply.Data["signUp"].(map[string]interface{})
		require.Equal(t, "Bob", user["name"])
		require.NotNil(t, user["posts"], "回查嵌套选择集应生效")
	}
}
