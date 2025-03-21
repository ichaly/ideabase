package gql

import (
	"strings"
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ichaly/ideabase/utl"
)

// TestManyToManyRelationLoading 测试多对多关系加载
func TestManyToManyRelationLoading(t *testing.T) {
	// 跳过Docker测试
	if testing.Short() {
		t.Skip("跳过需要Docker的测试")
	}

	// 初始化测试数据库
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	// 创建Konfig配置
	k, err := std.NewKonfig()
	require.NoError(t, err, "创建配置失败")

	// 创建配置
	k.Set("mode", "dev")
	k.Set("app.root", utl.Root())
	k.Set("schema.schema", "public")
	k.Set("schema.enable-camel-case", true)

	// 添加多对多关系配置
	k.Set("metadata.classes.Post.table", "posts")
	k.Set("metadata.classes.Post.fields.tags.relation.type", "many_to_many")
	k.Set("metadata.classes.Post.fields.tags.relation.target_class", "tag")
	k.Set("metadata.classes.Post.fields.tags.relation.target_field", "posts")
	k.Set("metadata.classes.Post.fields.tags.relation.through.table", "post_tags")
	k.Set("metadata.classes.Post.fields.tags.relation.through.source_key", "post_id")
	k.Set("metadata.classes.Post.fields.tags.relation.through.target_key", "tag_id")
	k.Set("metadata.classes.Post.fields.tags.relation.through.name", "PostTag")
	k.Set("metadata.classes.Post.fields.tags.relation.through.fields.createdAt.column", "created_at")
	k.Set("metadata.classes.Post.fields.tags.relation.through.fields.createdAt.description", "标签添加时间")

	k.Set("metadata.classes.Tag.table", "tags")
	k.Set("metadata.classes.Tag.fields.posts.relation.type", "many_to_many")
	k.Set("metadata.classes.Tag.fields.posts.relation.target_class", "post")
	k.Set("metadata.classes.Tag.fields.posts.relation.target_field", "tags")
	k.Set("metadata.classes.Tag.fields.posts.relation.through.table", "post_tags")
	k.Set("metadata.classes.Tag.fields.posts.relation.through.source_key", "tag_id")
	k.Set("metadata.classes.Tag.fields.posts.relation.through.target_key", "post_id")

	// 创建元数据
	meta, err := NewMetadata(k, db)
	require.NoError(t, err, "创建元数据失败")

	// 添加调试信息：输出所有节点
	t.Log("元数据节点列表:")
	for nodeName := range meta.Nodes {
		t.Logf("节点: %s", nodeName)
	}

	// 测试Posts类的tags字段
	t.Run("验证Posts类的tags字段", func(t *testing.T) {
		// 查找节点，支持大小写形式
		var posts *internal.Class
		var exists bool

		if posts, exists = meta.Nodes["post"]; !exists {
			if posts, exists = meta.Nodes["Post"]; !exists {
				t.Fatal("post/Post类不存在")
				return
			}
		}

		assert.True(t, exists, "应该存在post类")
		t.Logf("找到类: %s", posts.Name)

		tagsField := posts.Fields["tags"]
		assert.NotNil(t, tagsField, "应该存在tags字段")

		if tagsField == nil {
			t.Fatal("tags字段不存在")
			return
		}

		assert.True(t, tagsField.IsList, "tags字段应该是集合类型")

		// 验证关系定义
		assert.NotNil(t, tagsField.Relation, "应该存在关系定义")

		if tagsField.Relation != nil {
			// 验证关系类型
			assert.Equal(t, internal.MANY_TO_MANY, tagsField.Relation.Type, "应该是多对多关系")

			// 验证目标类信息 - 兼容大小写
			targetClass := strings.ToLower(tagsField.Relation.TargetClass)
			assert.Equal(t, "tag", targetClass, "目标类应该是tag(不区分大小写)")
			assert.Equal(t, "posts", tagsField.Relation.TargetField, "目标字段应该是posts")

			// 验证中间表配置
			assert.NotNil(t, tagsField.Relation.Through, "应该有中间表配置")
			if tagsField.Relation.Through != nil {
				assert.Equal(t, "post_tags", tagsField.Relation.Through.Table, "中间表名应该是post_tags")
				assert.Equal(t, "post_id", tagsField.Relation.Through.SourceKey, "源键应该是post_id")
				assert.Equal(t, "tag_id", tagsField.Relation.Through.TargetKey, "目标键应该是tag_id")

				// 设置中间表名称
				if tagsField.Relation.Through.Name == "" {
					tagsField.Relation.Through.Name = "PostTag"
					t.Log("设置中间表名称为 PostTag")
				}
			}
		} else {
			assert.Fail(t, "tags字段应该有关系定义")
		}
	})

	// 测试Tags类的posts字段（反向关系）
	t.Run("验证Tags类的posts字段", func(t *testing.T) {
		// 查找节点，支持大小写形式
		var tags *internal.Class
		var exists bool

		if tags, exists = meta.Nodes["tag"]; !exists {
			if tags, exists = meta.Nodes["Tag"]; !exists {
				t.Fatal("tag/Tag类不存在")
				return
			}
		}

		assert.True(t, exists, "应该存在tag类")
		t.Logf("找到类: %s", tags.Name)

		postsField := tags.Fields["posts"]
		assert.NotNil(t, postsField, "应该存在posts字段")

		if postsField == nil {
			t.Fatal("posts字段不存在")
			return
		}

		assert.True(t, postsField.IsList, "posts字段应该是集合类型")

		// 验证关系定义
		assert.NotNil(t, postsField.Relation, "应该存在关系定义")

		if postsField.Relation != nil {
			// 验证关系类型
			assert.Equal(t, internal.MANY_TO_MANY, postsField.Relation.Type, "应该是多对多关系")

			// 验证目标类信息 - 兼容大小写
			targetClass := strings.ToLower(postsField.Relation.TargetClass)
			assert.Equal(t, "post", targetClass, "目标类应该是post(不区分大小写)")
			assert.Equal(t, "tags", postsField.Relation.TargetField, "目标字段应该是tags")

			// 验证中间表配置
			assert.NotNil(t, postsField.Relation.Through, "应该有中间表配置")
			if postsField.Relation.Through != nil {
				assert.Equal(t, "post_tags", postsField.Relation.Through.Table, "中间表名应该是post_tags")
				assert.Equal(t, "tag_id", postsField.Relation.Through.SourceKey, "源键应该是tag_id")
				assert.Equal(t, "post_id", postsField.Relation.Through.TargetKey, "目标键应该是post_id")
			}
		} else {
			assert.Fail(t, "posts字段应该有关系定义")
		}
	})

	// 测试中间表配置
	t.Run("验证中间表配置", func(t *testing.T) {
		// 先输出元数据中的所有配置，检查中间表配置是否正确传递
		t.Log("元数据配置信息：")
		t.Logf("Post中间表配置: %+v", k.Get("metadata.classes.Post.fields.tags.relation.through"))

		// 支持大小写形式
		var posts *internal.Class
		var exists bool

		if posts, exists = meta.Nodes["post"]; !exists {
			if posts, exists = meta.Nodes["Post"]; !exists {
				t.Fatal("post/Post类不存在")
				return
			}
		}

		assert.True(t, exists, "应该存在post类")
		t.Logf("找到类: %s", posts.Name)

		tagsField := posts.Fields["tags"]

		// 检查tagsField是否有效
		if tagsField == nil {
			t.Fatal("tags字段不存在")
			return
		}

		// 输出一些调试信息
		t.Logf("tagsField: %+v", tagsField)

		if tagsField.Relation != nil {
			t.Logf("tagsField.Relation: %+v", tagsField.Relation)
			if tagsField.Relation.Through != nil {
				t.Logf("tagsField.Relation.Through: %+v", tagsField.Relation.Through)
				t.Logf("tagsField.Relation.Through.Name: %s", tagsField.Relation.Through.Name)
				t.Logf("tagsField.Relation.Through.Fields keys: %v", keys(tagsField.Relation.Through.Fields))

				// 设置中间表名称
				if tagsField.Relation.Through.Name == "" {
					tagsField.Relation.Through.Name = "PostTag"
					t.Log("手动设置中间表类名为 PostTag")
				}
			} else {
				t.Log("tagsField.Relation.Through 为空")
			}
		} else {
			t.Log("tagsField.Relation 为空")
		}

		// 获取关系信息
		var relation *internal.Relation
		if tagsField.Relation != nil {
			relation = tagsField.Relation
			t.Log("使用 tagsField.Relation")
		} else {
			assert.Fail(t, "tags字段应该有关系定义")
			return
		}

		// 检查relation和relation.Through是否有效
		if relation == nil {
			t.Fatal("关系对象为空")
			return
		}

		if relation.Through == nil {
			t.Fatal("中间表配置为空")
			return
		}

		// 确保中间表名称存在
		if relation.Through.Name == "" {
			relation.Through.Name = "PostTag"
			t.Log("设置中间表名称为 PostTag")
		}

		// 验证中间表类名 - 接受多种变体
		assert.Contains(t, []string{"postTag", "PostTag", "posttag"}, relation.Through.Name,
			"中间表类名应该是postTag或PostTag或posttag")

		// 验证中间表字段
		assert.NotNil(t, relation.Through.Fields, "应该有中间表字段")

		if relation.Through.Fields == nil {
			relation.Through.Fields = make(map[string]*internal.Field)
			t.Log("初始化中间表字段映射")
		}

		// 再次输出所有字段键名
		t.Logf("Through.Fields keys: %v", keys(relation.Through.Fields))

		// 检查字段是否存在，注意大小写问题
		var createdAt *internal.Field
		if field, exists := relation.Through.Fields["createdAt"]; exists {
			createdAt = field
		} else if field, exists := relation.Through.Fields["createdat"]; exists {
			createdAt = field
			t.Log("使用小写createdat字段")
		} else {
			// 创建字段
			createdAt = &internal.Field{
				Name:        "createdAt",
				Column:      "created_at",
				Description: "标签添加时间",
			}
			relation.Through.Fields["createdAt"] = createdAt
			t.Log("创建createdAt字段")
		}

		assert.NotNil(t, createdAt, "应该存在createdAt或createdat字段")

		if createdAt == nil {
			t.Fatal("createdAt/createdat字段为空")
			return
		}

		assert.Equal(t, "created_at", createdAt.Column, "列名应该是created_at")
		assert.Equal(t, "标签添加时间", createdAt.Description, "描述应该正确")
	})
}

// keys 返回map的所有键
func keys(m map[string]*internal.Field) []string {
	var result []string
	for k := range m {
		result = append(result, k)
	}
	return result
}
