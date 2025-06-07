package gql

import (
	"github.com/ichaly/ideabase/gql/metadata"
	"testing"

	"github.com/ichaly/ideabase/std"
	"github.com/ichaly/ideabase/utl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetadataLoadFromDatabase_CamelCase(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	k, err := std.NewKonfig()
	require.NoError(t, err, "创建配置失败")
	k.Set("mode", "dev")
	k.Set("app.root", utl.Root())
	k.Set("schema.schema", "public")
	k.Set("metadata.use-camel", true)

	meta, err := NewMetadata(k, db, WithoutLoader(
		metadata.LoaderMysql, metadata.LoaderFile,
	))
	require.NoError(t, err, "创建元数据加载器失败")
	assert.NotEmpty(t, meta.Nodes, "应该从数据库加载到元数据")

	list := []string{"User", "Post", "Tag", "PostTag"}
	for _, className := range list {
		class, exists := meta.Nodes[className]
		assert.True(t, exists, "应该存在表 %s", className)
		if exists {
			assert.NotEmpty(t, class.Fields, "表 %s 应该有字段", className)
		}
	}

	// 验证表名、类名、别名三种索引的指针一致性
	for _, className := range list {
		class, exists := meta.Nodes[className]
		if !exists {
			continue
		}
		table, exists := meta.Nodes[class.Table]
		if exists {
			assert.Same(t, class, table, "类名 %s 和表名 %s 应该指向同一个Class实例", class.Name, class.Table)
		}
		for fieldName, field := range class.Fields {
			column, exists := class.Fields[field.Column]
			if exists {
				assert.Same(t, field, column, "字段名 %s 和列名 %s 应该指向同一个Field实例", fieldName, field.Column)
			}
		}
	}

	// 验证字段命名为驼峰
	post, exists := meta.Nodes["Post"]
	assert.True(t, exists, "应该存在Post表")
	if exists {
		// 同时检查驼峰和下划线命名
		userIdCamel, okCamel := post.Fields["userId"]
		assert.True(t, okCamel, "字段userId应存在(驼峰)")
		userIdSnake, okSnake := post.Fields["user_id"]
		assert.True(t, okSnake, "字段user_id应同时存在(下划线)")

		// 两个命名应指向同一实例
		if okCamel && okSnake {
			assert.Same(t, userIdCamel, userIdSnake, "userId和user_id应指向同一个Field实例")
		}
	}
}

func TestMetadataLoadFromDatabase_NoCamelCase(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	k, err := std.NewKonfig()
	require.NoError(t, err, "创建配置失败")
	k.Set("mode", "dev")
	k.Set("app.root", utl.Root())
	k.Set("schema.schema", "public")
	k.Set("metadata.use-camel", false)
	k.Set("metadata.use-singular", false)

	meta, err := NewMetadata(k, db, WithoutLoader(
		metadata.LoaderMysql, metadata.LoaderFile,
	))
	require.NoError(t, err, "创建元数据加载器失败")
	assert.NotEmpty(t, meta.Nodes, "应该从数据库加载到元数据")

	// 禁用驼峰命名时，应检查驼峰类名不存在，原始表名存在
	camelNames := []string{"User", "Post", "Tag", "PostTag"}
	snakeNames := []string{"users", "posts", "tags", "post_tags"}

	// 验证驼峰命名的类名不存在
	for _, camelName := range camelNames {
		_, exists := meta.Nodes[camelName]
		assert.False(t, exists, "驼峰类名 %s 不应存在", camelName)
	}

	// 验证原始表名存在
	for _, tableName := range snakeNames {
		class, exists := meta.Nodes[tableName]
		assert.True(t, exists, "表名 %s 应存在", tableName)
		if exists {
			assert.NotEmpty(t, class.Fields, "表 %s 应该有字段", tableName)
		}
	}

	// 表名索引检查
	for _, tableName := range snakeNames {
		class, exists := meta.Nodes[tableName]
		if !exists {
			continue
		}

		// 字段名和列名索引应指向同一个Field指针
		for fieldName, field := range class.Fields {
			column, exists := class.Fields[field.Column]
			if exists {
				assert.Same(t, field, column, "字段名 %s 和列名 %s 应该指向同一个Field实例", fieldName, field.Column)
			}
		}
	}

	// 验证字段仅有下划线命名，无驼峰命名
	posts, exists := meta.Nodes["posts"]
	assert.True(t, exists, "应该存在posts表")
	if exists {
		// 验证字段名为下划线形式
		_, okCamel := posts.Fields["userId"]
		assert.False(t, okCamel, "字段userId不应存在(驼峰)")
		_, okSnake := posts.Fields["user_id"]
		assert.True(t, okSnake, "字段user_id应存在(下划线)")

		// 额外检查其他字段
		_, hasTitle := posts.Fields["title"]
		assert.True(t, hasTitle, "应有title字段")
		_, hasCreatedAt := posts.Fields["created_at"]
		assert.True(t, hasCreatedAt, "应有created_at字段")
	}
}
