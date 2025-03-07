package gql

import (
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/utl"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	v.Set("metadata.tables.posts.columns.tags", map[string]interface{}{
		"name": "tags",
		"relation": map[string]interface{}{
			"type":        "many_to_many",
			"targetClass": "Tags",
			"targetField": "posts",
			"through": map[string]interface{}{
				"table":     "post_tags",
				"sourceKey": "post_id",
				"targetKey": "tag_id",
			},
		},
	})

	// 创建元数据加载器
	meta, err := NewMetadata(v, db)
	require.NoError(t, err, "创建元数据加载器失败")

	// 测试Posts类的tags字段
	t.Run("验证Posts类的tags字段", func(t *testing.T) {
		posts, exists := meta.Nodes["Posts"]
		assert.True(t, exists, "应该存在Posts类")

		tagsField := posts.GetField("tags")
		assert.NotNil(t, tagsField, "应该存在tags字段")
		assert.NotNil(t, tagsField.Relation, "tags字段应该有关系定义")

		// 验证关系类型
		assert.Equal(t, internal.MANY_TO_MANY, tagsField.Relation.Type, "应该是多对多关系")

		// 验证目标类信息
		assert.Equal(t, "Tags", tagsField.Relation.TargetClass, "目标类应该是Tags")
		assert.Equal(t, "posts", tagsField.Relation.TargetField, "目标字段应该是posts")

		// 验证中间表配置
		assert.NotNil(t, tagsField.Relation.Through, "应该有中间表配置")
		assert.Equal(t, "post_tags", tagsField.Relation.Through.Table, "中间表名应该是post_tags")
		assert.Equal(t, "post_id", tagsField.Relation.Through.SourceKey, "源键应该是post_id")
		assert.Equal(t, "tag_id", tagsField.Relation.Through.TargetKey, "目标键应该是tag_id")
	})

	// 测试Tags类的posts字段（反向关系）
	t.Run("验证Tags类的posts字段", func(t *testing.T) {
		tags, exists := meta.Nodes["Tags"]
		assert.True(t, exists, "应该存在Tags类")

		postsField := tags.GetField("posts")
		assert.NotNil(t, postsField, "应该存在posts字段")
		assert.NotNil(t, postsField.Relation, "posts字段应该有关系定义")

		// 验证关系类型
		assert.Equal(t, internal.MANY_TO_MANY, postsField.Relation.Type, "应该是多对多关系")

		// 验证目标类信息
		assert.Equal(t, "Posts", postsField.Relation.TargetClass, "目标类应该是Posts")
		assert.Equal(t, "tags", postsField.Relation.TargetField, "目标字段应该是tags")

		// 验证中间表配置
		assert.NotNil(t, postsField.Relation.Through, "应该有中间表配置")
		assert.Equal(t, "post_tags", postsField.Relation.Through.Table, "中间表名应该是post_tags")
		assert.Equal(t, "tag_id", postsField.Relation.Through.SourceKey, "源键应该是tag_id")
		assert.Equal(t, "post_id", postsField.Relation.Through.TargetKey, "目标键应该是post_id")
	})

	// 测试双向关系
	t.Run("验证双向关系", func(t *testing.T) {
		posts := meta.Nodes["Posts"]
		tags := meta.Nodes["Tags"]

		tagsField := posts.GetField("tags")
		postsField := tags.GetField("posts")

		// 验证双向引用
		assert.Equal(t, tagsField.Relation, postsField.Relation.Reverse, "Posts.tags应该是Tags.posts的反向关系")
		assert.Equal(t, postsField.Relation, tagsField.Relation.Reverse, "Tags.posts应该是Posts.tags的反向关系")
	})
}
