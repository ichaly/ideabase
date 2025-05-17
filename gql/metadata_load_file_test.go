package gql

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ichaly/ideabase/std"
	"github.com/ichaly/ideabase/utl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetadataLoadFromFile(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	k, err := std.NewKonfig()
	require.NoError(t, err, "创建配置失败")
	k.Set("mode", "dev")
	k.Set("app.root", utl.Root())

	meta, err := NewMetadata(k, db)
	require.NoError(t, err, "创建元数据加载器失败")

	// 保存到文件
	filePath := filepath.Join(utl.Root(), "cfg", "metadata.test.json")
	err = meta.saveToFile(filePath)
	require.NoError(t, err, "保存元数据到文件失败")
	defer os.Remove(filePath)

	// 从文件加载
	k.Set("mode", "test")
	k.Set("metadata.file", "cfg/metadata.test.json")
	meta2, err := NewMetadata(k, nil)
	require.NoError(t, err, "从文件创建元数据加载器失败")
	assert.NotEmpty(t, meta2.Nodes, "元数据不应为空")

	// 验证节点数量和版本
	assert.Equal(t, len(meta.Nodes), len(meta2.Nodes), "节点数量应该相同")
	assert.Equal(t, meta.Version, meta2.Version, "版本应该相同")

	// 验证表名、类名、别名三种索引的指针一致性
	for _, class := range meta2.Nodes {
		tablePtr, tableExists := meta2.Nodes[class.Table]
		if tableExists && class.Table != "" {
			assert.Same(t, class, tablePtr, "类名 %s 和表名 %s 应该指向同一个Class实例", class.Name, class.Table)
		}
		// 别名（如有）应为新指针
		for alias, node := range meta2.Nodes {
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
