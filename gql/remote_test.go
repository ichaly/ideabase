package gql

import (
	"context"
	"strings"
	"testing"

	_ "github.com/ichaly/ideabase/gql/compiler/pgsql" // 自注册PostgreSQL方言
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// profileAPI 测试远程数据源：按用户id返回画像
type profileAPI struct{ data map[any]any }

func (profileAPI) Name() string { return "profile-api" }
func (my profileAPI) Fetch(_ context.Context, keys []any) (map[any]any, error) {
	out := make(map[any]any, len(keys))
	for _, k := range keys {
		if v, ok := my.data[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

// TestRemoteJoin 远程关系端到端：配置声明→schema渲染（无查询面）→
// 自动补投影→批量取数回填→内部键剥除→未注册/键缺失容错置null
func TestRemoteJoin(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, func(k *std.Konfig) {
		k.Set("metadata.classes", map[string]*internal.ClassConfig{
			"Profile": {
				Description: "用户画像(远程虚拟类)",
				Fields: map[string]*internal.FieldConfig{
					"level": {Type: "String"},
				},
			},
			"User": {
				Table: "users",
				Fields: map[string]*internal.FieldConfig{
					"profile": {
						Type:   "Profile",
						Remote: &internal.RemoteConfig{Source: "profile-api", Key: "id"},
					},
				},
			},
		})
	})
	defer cleanup()
	ctx := context.Background()

	// schema契约：虚拟类只有类型定义与字段，没有查询面
	require.Contains(t, executor.source, "type Profile {", "远程目标类应有类型定义")
	require.Contains(t, executor.source, "profile: Profile", "远程字段应挂在宿主类型上")
	assert.NotContains(t, executor.source, "profiles(", "虚拟类不应有查询根")
	assert.NotContains(t, executor.source, "ProfileWhereInput", "虚拟类不应有过滤器")
	assert.NotContains(t, executor.source, "profile: SortDirection", "远程字段无SQL排序能力,不应进SortInput")

	var ids []int64
	for _, name := range []string{"u1", "u2"} {
		reply := executor.Execute(ctx, `mutation ($n: String!, $e: String!) {
			createUser(input: { name: $n, email: $e }) { id }
		}`, map[string]interface{}{"n": name, "e": name + "@x.com"}, "")
		require.Empty(t, reply.Errors)
		ids = append(ids, reply.Data["createUser"].(map[string]interface{})["id"].(int64))
	}

	query := `query { users(sort: [{ id: ASC }]) { items { name profile { level } } } }`

	// 数据源未注册：字段置null + 警告随errors返回，data与错误共存（部分错误语义）
	reply := executor.Execute(ctx, query, nil, "")
	require.Len(t, reply.Errors, 1, "应有一条远程警告")
	assert.Contains(t, reply.Errors[0].Message, "未注册")
	require.NotNil(t, reply.Data, "警告不应吞掉data")
	items := reply.Data["users"].(map[string]interface{})["items"].([]interface{})
	require.Len(t, items, 2)
	assert.Nil(t, items[0].(map[string]interface{})["profile"])

	// 注册后：批量取数按键回填；u2故意缺失验证键未命中置null
	executor.RegisterRemote(profileAPI{data: map[any]any{
		ids[0]: map[string]interface{}{"level": "gold"},
	}})
	reply = executor.Execute(ctx, query, nil, "")
	require.Empty(t, reply.Errors, "远程回填失败: %v", reply.Errors)
	items = reply.Data["users"].(map[string]interface{})["items"].([]interface{})

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
