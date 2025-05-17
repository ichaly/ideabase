package gql

import (
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/std"
	"github.com/ichaly/ideabase/utl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetadataLoadFromDatabase(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	k, err := std.NewKonfig()
	require.NoError(t, err, "创建配置失败")
	k.Set("mode", "dev")
	k.Set("app.root", utl.Root())
	k.Set("schema.schema", "public")
	k.Set("schema.enable-camel-case", true)

	meta, err := NewMetadata(k, db)
	require.NoError(t, err, "创建元数据加载器失败")
	assert.NotEmpty(t, meta.Nodes, "应该从数据库加载到元数据")

	// 验证表的加载
	tables := []string{"User", "Post", "Tag", "PostTag"}
	for _, tableName := range tables {
		class, exists := meta.Nodes[tableName]
		assert.True(t, exists, "应该存在表 %s", tableName)
		if exists {
			assert.NotEmpty(t, class.Fields, "表 %s 应该有字段", tableName)
		}
	}

	// 验证关系加载
	post, exists := meta.Nodes["Post"]
	assert.True(t, exists, "应该存在Post表")
	if exists {
		userId := post.Fields["userId"]
		assert.NotNil(t, userId, "应该有userId字段")
		if userId != nil {
			assert.NotNil(t, userId.Relation, "userId应该有关系定义")
			assert.Equal(t, internal.MANY_TO_ONE, userId.Relation.Type, "应该是many-to-one关系")
			assert.Equal(t, "User", userId.Relation.TargetClass, "关系目标类应该是User")
		}
	}

	// 验证表名、类名、别名三种索引的指针一致性
	for _, tableName := range tables {
		class, exists := meta.Nodes[tableName]
		if !exists {
			continue
		}
		// 表名和类名索引应指向同一个指针
		tablePtr, tableExists := meta.Nodes[class.Table]
		if tableExists {
			assert.Same(t, class, tablePtr, "类名 %s 和表名 %s 应该指向同一个Class实例", class.Name, class.Table)
		}
		// 别名（如有）应为新指针
		for name, node := range meta.Nodes {
			if name != class.Name && name != class.Table && node.Table == class.Table {
				assert.NotSame(t, class, node, "别名 %s 应该是新的Class指针", name)
			}
		}
		// 字段名和列名索引应指向同一个Field指针
		for fieldName, field := range class.Fields {
			if field.Column != "" {
				colPtr, colExists := class.Fields[field.Column]
				if colExists {
					assert.Same(t, field, colPtr, "字段名 %s 和列名 %s 应该指向同一个Field实例", fieldName, field.Column)
				}
			}
		}
	}
}
