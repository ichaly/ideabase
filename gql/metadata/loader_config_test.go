package metadata

import (
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHoster 测试用元数据承载者
type fakeHoster struct {
	nodes   map[string]*protocol.Class
	version string
}

func newFakeHoster() *fakeHoster {
	return &fakeHoster{nodes: make(map[string]*protocol.Class)}
}

func (my *fakeHoster) PutNode(name string, node *protocol.Class) error {
	my.nodes[name] = node
	return nil
}

func (my *fakeHoster) GetNode(name string) (*protocol.Class, bool) {
	node, ok := my.nodes[name]
	return node, ok
}

func (my *fakeHoster) SetVersion(version string) { my.version = version }

// TestConfigLoaderNoMutation 配置对象进程常驻且跨次构建复用：
// Load回填列名只能用局部变量，绝不能写回fieldConfig.Column，
// 否则二次构建时字段分组判定漂移（虚拟字段被当作列字段）
func TestConfigLoaderNoMutation(t *testing.T) {
	// total在配置里是虚拟字段（Column空），但基础类恰好有同名列字段——
	// 旧实现会把fieldConfig.Column就地改写成"total"
	cfg := &internal.Config{Metadata: internal.MetadataConfig{
		UseCamel: true,
		Classes: map[string]*internal.ClassConfig{
			"User": {Table: "users", Fields: map[string]*internal.FieldConfig{
				"total": {Type: "integer", Description: "虚拟统计字段"},
			}},
		},
	}}
	loader := NewConfigLoader(cfg)

	build := func() *protocol.Class {
		h := newFakeHoster()
		// 模拟db加载器产出的基础类（每次构建重新生成，与生产流程一致）
		require.NoError(t, h.PutNode("users", &protocol.Class{
			Name: "users", Table: "users", Fields: map[string]*protocol.Field{
				"total": {Name: "total", Column: "total", Type: "bigint"},
			},
		}))
		require.NoError(t, loader.Load(h))
		class, ok := h.GetNode("users")
		require.True(t, ok)
		return class
	}

	first := build()
	assert.Empty(t, cfg.Metadata.Classes["User"].Fields["total"].Column,
		"Load不得改写常驻配置对象的Column")

	second := build()
	assert.Equal(t, first.Fields["total"].Column, second.Fields["total"].Column,
		"两次构建的字段列名应一致（无分组漂移）")
	assert.Equal(t, first.Fields["total"].Type, second.Fields["total"].Type,
		"两次构建的字段类型应一致")
	assert.Equal(t, len(first.Fields), len(second.Fields), "两次构建的字段集合应一致")
}
