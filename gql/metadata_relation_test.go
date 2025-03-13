package gql

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/utl"
)

// TestManyToManyRelationLoading 测试多对多关系的配置加载
func TestManyToManyRelationLoading(t *testing.T) {
	// 初始化测试数据库
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	// 创建配置
	v := viper.New()
	v.Set("mode", "dev")
	v.Set("app.root", utl.Root())
	v.Set("schema.schema", "public")
	v.Set("schema.enable-camel-case", true)

	// 设置多对多关系配置
	v.Set("metadata.classes", map[string]map[string]interface{}{
		"Post": {
			"table": "posts",
			"fields": map[string]map[string]interface{}{
				"tags": {
					"virtual": true,
					"relation": map[string]interface{}{
						"type":         "many_to_many",
						"target_class": "Tag",
						"target_field": "posts",
						"through": map[string]interface{}{
							"table":      "post_tags",
							"source_key": "post_id",
							"target_key": "tag_id",
							"class_name": "PostTag",
							"fields": map[string]map[string]interface{}{
								"createdAt": {
									"column":      "created_at",
									"type":        "timestamp",
									"description": "标签添加时间",
								},
							},
						},
					},
				},
			},
		},
		"Tag": {
			"table": "tags",
			"fields": map[string]map[string]interface{}{
				"posts": {
					"virtual": true,
					"relation": map[string]interface{}{
						"type":         "many_to_many",
						"target_class": "Post",
						"target_field": "tags",
						"through": map[string]interface{}{
							"table":      "post_tags",
							"source_key": "tag_id",
							"target_key": "post_id",
							"class_name": "PostTag",
						},
					},
				},
			},
		},
	})

	// 创建元数据加载器
	meta, err := NewMetadata(v, db)
	require.NoError(t, err, "创建元数据加载器失败")

	// 测试Posts类的tags字段
	t.Run("验证Posts类的tags字段", func(t *testing.T) {
		posts, exists := meta.Nodes["Post"]
		assert.True(t, exists, "应该存在Post类")

		tagsField := posts.Fields["tags"]
		assert.NotNil(t, tagsField, "应该存在tags字段")
		assert.NotNil(t, tagsField.Relation, "tags字段应该有关系定义")

		// 验证关系类型
		assert.Equal(t, internal.MANY_TO_MANY, tagsField.Relation.Type, "应该是多对多关系")

		// 验证目标类信息
		assert.Equal(t, "Tag", tagsField.Relation.TargetClass, "目标类应该是Tag")
		assert.Equal(t, "posts", tagsField.Relation.TargetField, "目标字段应该是posts")

		// 验证中间表配置
		assert.NotNil(t, tagsField.Relation.Through, "应该有中间表配置")
		assert.Equal(t, "post_tags", tagsField.Relation.Through.Table, "中间表名应该是post_tags")
		assert.Equal(t, "post_id", tagsField.Relation.Through.SourceKey, "源键应该是post_id")
		assert.Equal(t, "tag_id", tagsField.Relation.Through.TargetKey, "目标键应该是tag_id")
	})

	// 测试Tags类的posts字段（反向关系）
	t.Run("验证Tags类的posts字段", func(t *testing.T) {
		tags, exists := meta.Nodes["Tag"]
		assert.True(t, exists, "应该存在Tag类")

		postsField := tags.Fields["posts"]
		assert.NotNil(t, postsField, "应该存在posts字段")
		assert.NotNil(t, postsField.Relation, "posts字段应该有关系定义")

		// 验证关系类型
		assert.Equal(t, internal.MANY_TO_MANY, postsField.Relation.Type, "应该是多对多关系")

		// 验证目标类信息
		assert.Equal(t, "Post", postsField.Relation.TargetClass, "目标类应该是Post")
		assert.Equal(t, "tags", postsField.Relation.TargetField, "目标字段应该是tags")

		// 验证中间表配置
		assert.NotNil(t, postsField.Relation.Through, "应该有中间表配置")
		assert.Equal(t, "post_tags", postsField.Relation.Through.Table, "中间表名应该是post_tags")
		assert.Equal(t, "tag_id", postsField.Relation.Through.SourceKey, "源键应该是tag_id")
		assert.Equal(t, "post_id", postsField.Relation.Through.TargetKey, "目标键应该是post_id")
	})

	// 测试双向关系
	t.Run("验证双向关系", func(t *testing.T) {
		posts := meta.Nodes["Post"]
		tags := meta.Nodes["Tag"]

		tagsField := posts.Fields["tags"]
		postsField := tags.Fields["posts"]

		// 验证双向引用
		assert.Equal(t, tagsField.Relation, postsField.Relation.Reverse, "Post.tags应该是Tag.posts的反向关系")
		assert.Equal(t, postsField.Relation, tagsField.Relation.Reverse, "Tag.posts应该是Post.tags的反向关系")
	})

	// 测试中间表配置
	t.Run("验证中间表配置", func(t *testing.T) {
		posts := meta.Nodes["Post"]
		tagsField := posts.Fields["tags"]

		// 验证中间表类名
		assert.Equal(t, "PostTag", tagsField.Relation.Through.Name, "中间表类名应该是PostTag")

		// 验证中间表字段
		assert.NotNil(t, tagsField.Relation.Through.Fields, "应该有中间表字段")

		createdAt := tagsField.Relation.Through.Fields["createdAt"]
		assert.NotNil(t, createdAt, "应该存在createdAt字段")
		assert.Equal(t, "created_at", createdAt.Column, "列名应该是created_at")
		assert.Equal(t, "标签添加时间", createdAt.Description, "描述应该正确")
	})
}
