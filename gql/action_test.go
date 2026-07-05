package gql

import (
	"context"
	"fmt"
	"testing"

	_ "github.com/ichaly/ideabase/gql/compiler/pgsql" // 自注册PostgreSQL方言
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// pingAction 标量直通型Action：无回查，结果原样输出
type pingAction struct{}

func (pingAction) Define() Define {
	return Define{Name: "ping", Args: "msg: String!", Result: "String", Query: true}
}
func (pingAction) Execute(_ context.Context, args map[string]interface{}) (interface{}, error) {
	return "pong:" + args["msg"].(string), nil
}

// signUpReq 参数即声明：json定名，validate含required渲染为非空!
type signUpReq struct {
	Name  string `json:"name" validate:"required"`
	Email string `json:"email" validate:"required"`
}

// newSignUpAction 编排型Action（NewAction反射装配）：建号返回id，引擎按选择集回查补全；
// 返回类型int64本推导为Int，Result选项覆盖为实体User触发回查
func newSignUpAction(db *gorm.DB) Action {
	return NewAction("signUp", "建号并回查主档", func(ctx context.Context, req signUpReq) (int64, error) {
		var id int64
		err := db.WithContext(ctx).
			Raw("INSERT INTO users(name, email) VALUES(?, ?) RETURNING id", req.Name, req.Email).
			Scan(&id).Error
		return id, err
	}, Result("User"))
}

// setupActionExecutor 与setupTestExecutor同构，但透出db供Action闭包使用
func setupActionExecutor(t *testing.T) (*Executor, *gorm.DB, func()) {
	return newTestExecutor(t, nil)
}

// TestActionRoundTrip Action端到端：注册→内省可见→分发执行→回查补全→混排拒绝
func TestActionRoundTrip(t *testing.T) {
	executor, db, cleanup := setupActionExecutor(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, executor.Register(pingAction{}, newSignUpAction(db)))

	// 1. 内省可见：自定义字段与表CRUD一视同仁
	reply := executor.Execute(ctx, `{ __type(name: "Mutation") { fields { name } } }`, nil, "")
	require.Empty(t, reply.Errors, "自省失败: %v", reply.Errors)
	fields := reply.Data["__type"].(map[string]interface{})["fields"].([]interface{})
	var names []string
	for _, f := range fields {
		names = append(names, f.(map[string]interface{})["name"].(string))
	}
	require.Contains(t, names, "signUp", "Action应出现在Mutation自省结果中")

	// 2. 标量直通：结果原样输出（连跑两次覆盖计划缓存命中路径）
	for i := 0; i < 2; i++ {
		reply = executor.Execute(ctx, `query { ping(msg: "hi") }`, nil, "")
		require.Empty(t, reply.Errors, "ping执行失败: %v", reply.Errors)
		require.Equal(t, "pong:hi", reply.Data["ping"])
	}

	// 3. 回查补全：Action返回id，引擎按客户端选择集（含别名与关系）读回实体
	reply = executor.Execute(ctx, `mutation ($n: String!, $e: String!) {
		u: signUp(name: $n, email: $e) { id name posts { title } }
	}`, map[string]interface{}{"n": "Alice", "e": "alice@x.com"}, "")
	require.Empty(t, reply.Errors, "signUp执行失败: %v", reply.Errors)
	user := reply.Data["u"].(map[string]interface{})
	require.Equal(t, "Alice", user["name"])
	require.NotNil(t, user["id"])
	require.NotNil(t, user["posts"], "关系字段应随回查补全")

	// 4. 变量校验：未注册字段仍被schema校验拦截
	reply = executor.Execute(ctx, `mutation { nosuch(x: 1) }`, nil, "")
	require.NotEmpty(t, reply.Errors, "未定义字段应报错")

	// 5. 混排拒绝：Action与实体字段不可同请求
	reply = executor.Execute(ctx, `query { ping(msg: "x") users { total } }`, nil, "")
	require.NotEmpty(t, reply.Errors, "混排应报错")
	require.Contains(t, reply.Errors.Error(), "混排")
}

// TestActionTypename __typename与Action共存（Apollo客户端默认注入）：
// 回归——内省元字段曾被计入miss导致误判为「Action与实体字段混排」
func TestActionTypename(t *testing.T) {
	executor, db, cleanup := setupActionExecutor(t)
	defer cleanup()
	ctx := context.Background()
	require.NoError(t, executor.Register(pingAction{}, newSignUpAction(db)))

	// 查询操作回填"Query"
	reply := executor.Execute(ctx, `query { ping(msg: "hi") __typename }`, nil, "")
	require.Empty(t, reply.Errors, "__typename不应触发混排拒绝: %v", reply.Errors)
	require.Equal(t, "pong:hi", reply.Data["ping"])
	require.Equal(t, "Query", reply.Data["__typename"])

	// 变更操作回填"Mutation"（含别名）
	reply = executor.Execute(ctx, `mutation ($n: String!, $e: String!) {
		signUp(name: $n, email: $e) { id } t: __typename
	}`, map[string]interface{}{"n": "Ty", "e": "ty@x.com"}, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	require.Equal(t, "Mutation", reply.Data["t"])
}

// TestActionVariablePassthrough 回查合成查询透传原变量声明：
// 回归——对象/枚举变量经json内联会产生带引号键、带引号枚举的非法GraphQL字面量，
// 导致enrich合成回查解析失败
func TestActionVariablePassthrough(t *testing.T) {
	executor, db, cleanup := setupActionExecutor(t)
	defer cleanup()
	ctx := context.Background()
	require.NoError(t, executor.Register(newSignUpAction(db)))

	// 准备：建号并为其创建两篇文章（回查嵌套选择集有数据可过滤/排序）
	reply := executor.Execute(ctx, `mutation ($n: String!, $e: String!) {
		signUp(name: $n, email: $e) { id }
	}`, map[string]interface{}{"n": "Vera", "e": "vera@x.com"}, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	uid := reply.Data["signUp"].(map[string]interface{})["id"]
	for _, title := range []string{"B2", "A1"} {
		reply = executor.Execute(ctx, `mutation ($t: String!, $u: ID!) {
			createPost(input: { title: $t, userId: $u }) { id }
		}`, map[string]interface{}{"t": title, "u": uid}, "")
		require.Empty(t, reply.Errors, "%v", reply.Errors)
	}

	// 嵌套选择集引用对象+枚举变量（$s为[{title: ASC}]，json内联会产生带引号键
	// 与带引号枚举的非法字面量）与字段级标量变量（$lk），连跑两次覆盖回查计划缓存
	for i := 0; i < 2; i++ {
		reply = executor.Execute(ctx, `mutation ($n: String!, $e: String!, $lk: String, $s: [PostSortInput!]) {
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
