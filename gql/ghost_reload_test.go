package gql

import (
	"path/filepath"
	"testing"

	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
)

// 文件重载后字段集必须与保存时一致(processRelations重复生成会出user1/comments1幽灵)
func TestFileReloadFieldParity(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	k, err := std.NewKonfig()
	require.NoError(t, err)
	root := t.TempDir()
	k.Set("mode", "dev")
	k.Set("app.root", root)

	meta, err := NewMetadata(k, db)
	require.NoError(t, err)
	require.NoError(t, meta.saveToFile(filepath.Join(root, "cfg", "metadata.test.json")))

	k.Set("mode", "test")
	k.Set("metadata.file", "cfg/metadata.test.json")
	meta2, err := NewMetadata(k, nil)
	require.NoError(t, err)

	for name, class := range meta.Nodes {
		if name != class.Name {
			continue
		}
		class2 := meta2.Nodes[name]
		require.NotNil(t, class2, "类 %s 丢失", name)
		for fn, f := range class2.Fields {
			if fn != f.Name {
				continue
			}
			require.NotNil(t, class.Fields[fn], "类 %s 重载后多出幽灵字段 %s", name, fn)
		}
	}
}
