package gql

import (
	"context"
	"strings"
	"testing"

	_ "github.com/ichaly/ideabase/gql/compiler/pgsql" // 自注册PostgreSQL方言
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Profile 远程画像：虚拟类型SDL反射自此struct（Go类型名即GraphQL类型名，json标签定字段名）
type Profile struct {
	Level string `json:"level" doc:"等级"`
}

// TestRemoteJoin 远程关系端到端（注册即声明）：NewRemote挂载关系字段+反射虚拟类型→
// 无查询面→自动补投影→批量取数回填→内部键剥除→键缺失容错置null
func TestRemoteJoin(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	ctx := context.Background()

	data := map[any]Profile{}
	require.NoError(t, executor.Register(NewRemote("User", "profile", "用户画像", "id",
		func(_ context.Context, keys []any) (map[any]Profile, error) {
			out := make(map[any]Profile, len(keys))
			for _, k := range keys {
				if v, ok := data[k]; ok {
					out[k] = v
				}
			}
			return out, nil
		})))

	// schema契约：注册后虚拟类型与关系字段进schema，但没有查询面
	require.NotNil(t, executor.schema.Types["Profile"], "远程目标类型应进schema")
	require.NotNil(t, executor.schema.Types["User"].Fields.ForName("profile"), "远程字段应挂在宿主类型上")
	assert.Nil(t, executor.schema.Types["ProfileWhereInput"], "虚拟类型不应有过滤器")
	assert.Nil(t, executor.schema.Query.Fields.ForName("profiles"), "虚拟类型不应有查询根")

	var ids []int64
	for _, name := range []string{"u1", "u2"} {
		reply := executor.Execute(ctx, `mutation ($n: String!, $e: String!) {
			createUser(input: { name: $n, email: $e }) { id }
		}`, map[string]interface{}{"n": name, "e": name + "@x.com"}, "")
		require.Empty(t, reply.Errors)
		ids = append(ids, reply.Data["createUser"].(map[string]interface{})["id"].(int64))
	}
	data[ids[0]] = Profile{Level: "gold"} // u2故意缺失验证键未命中置null

	reply := executor.Execute(ctx, `query { users(sort: [{ id: ASC }]) { items { name profile { level } } } }`, nil, "")
	require.Empty(t, reply.Errors, "远程回填失败: %v", reply.Errors)
	items := reply.Data["users"].(map[string]interface{})["items"].([]interface{})
	require.Len(t, items, 2)

	first := items[0].(map[string]interface{})
	require.NotNil(t, first["profile"], "命中键应回填")
	assert.Equal(t, "gold", first["profile"].(map[string]interface{})["level"])
	assert.Nil(t, items[1].(map[string]interface{})["profile"], "键未命中置null")

	// 内部补投影键已剥除，响应形状严格等于选择集
	for _, item := range items {
		for k := range item.(map[string]interface{}) {
			assert.False(t, strings.HasPrefix(k, "__rk_"), "内部键应剥除: %s", k)
		}
	}
}

// TestRemoteSharedKey 两个远程字段共用同一宿主键列:内部键别名__rk_id被先回填者
// 剥除后,后回填者读不到键值恒为null——剥除必须在全部回填完成之后
func TestRemoteSharedKey(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	ctx := context.Background()

	fetch := func(level string) func(context.Context, []any) (map[any]Profile, error) {
		return func(_ context.Context, keys []any) (map[any]Profile, error) {
			out := make(map[any]Profile, len(keys))
			for _, k := range keys {
				out[k] = Profile{Level: level}
			}
			return out, nil
		}
	}
	require.NoError(t, executor.Register(
		NewRemote("User", "profileA", "画像A", "id", fetch("gold")),
		NewRemote("User", "profileB", "画像B", "id", fetch("silver")),
	))

	reply := executor.Execute(ctx, `mutation { createUser(input: { name: "S", email: "s@x.com" }) { id } }`, nil, "")
	require.Empty(t, reply.Errors)

	reply = executor.Execute(ctx, `query { users { items { name profileA { level } profileB { level } } } }`, nil, "")
	require.Empty(t, reply.Errors, "共享键双远程失败: %v", reply.Errors)
	item := reply.Data["users"].(map[string]interface{})["items"].([]interface{})[0].(map[string]interface{})
	require.NotNil(t, item["profileA"], "第一个远程字段应回填")
	require.NotNil(t, item["profileB"], "第二个远程字段应回填(共享键不被先回填者剥除)")
	assert.Equal(t, "gold", item["profileA"].(map[string]interface{})["level"])
	assert.Equal(t, "silver", item["profileB"].(map[string]interface{})["level"])
	assert.NotContains(t, item, "__rk_id", "内部键最终仍须剥除")
}
