package gql

import (
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/std"
	"github.com/ichaly/ideabase/utl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetadataLoadFromConfig(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	k, err := std.NewKonfig()
	require.NoError(t, err, "创建配置失败")
	k.Set("mode", "test")
	k.Set("app.root", utl.Root())
	k.Set("metadata.classes", map[string]*internal.ClassConfig{
		"User": {
			Table:       "users",
			Description: "用户表",
			Fields: map[string]*internal.FieldConfig{
				"id": {
					Column:      "id",
					Type:        "int",
					Description: "用户ID",
					IsPrimary:   true,
				},
				"name": {
					Column:      "name",
					Type:        "string",
					Description: "用户名",
				},
			},
		},
	})

	meta, err := NewMetadata(k, db)
	require.NoError(t, err, "创建元数据加载器失败")

	user, exists := meta.Nodes["User"]
	require.True(t, exists, "应该存在User类")
	assert.Equal(t, "用户表", user.Description, "类描述应该正确")
	assert.Equal(t, "users", user.Table, "表名应该正确")

	id, exists := user.Fields["id"]
	require.True(t, exists, "应该存在id字段")
	assert.Equal(t, "用户ID", id.Description, "id字段描述应该正确")

	// 验证表名、类名、别名三种索引的指针一致性
	for _, class := range meta.Nodes {
		tablePtr, tableExists := meta.Nodes[class.Table]
		if tableExists && class.Table != "" {
			assert.Same(t, class, tablePtr, "类名 %s 和表名 %s 应该指向同一个Class实例", class.Name, class.Table)
		}
		// 别名（如有）应为新指针
		for alias, node := range meta.Nodes {
			if alias != class.Name && alias != class.Table && node.Table == class.Table {
				assert.NotSame(t, class, node, "别名 %s 应该是新的Class指针", alias)
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
