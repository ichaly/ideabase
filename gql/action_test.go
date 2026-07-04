package gql

import (
	"context"
	"testing"

	_ "github.com/ichaly/ideabase/gql/compiler/pgsql" // 自注册PostgreSQL方言
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// pingAction 标量直通型Action：无回查，结果原样输出
type pingAction struct{}

func (pingAction) Name() string { return "ping" }
func (pingAction) Definition() string {
	return `extend type Query { ping(msg: String!): String }`
}
func (pingAction) Execute(_ context.Context, args map[string]interface{}) (interface{}, error) {
	return "pong:" + args["msg"].(string), nil
}

// signUpAction 编排型Action：Go侧建号返回id，验证引擎按选择集回查补全
type signUpAction struct{ db *gorm.DB }

func (signUpAction) Name() string { return "signUp" }
func (signUpAction) Definition() string {
	return `extend type Mutation { signUp(name: String!, email: String!): User }`
}
func (my signUpAction) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	var id int64
	err := my.db.WithContext(ctx).
		Raw("INSERT INTO users(name, email) VALUES(?, ?) RETURNING id", args["name"], args["email"]).
		Scan(&id).Error
	return id, err
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

	require.NoError(t, executor.RegisterAction(pingAction{}, signUpAction{db: db}))

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
