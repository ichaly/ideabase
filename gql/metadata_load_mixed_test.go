package gql

import (
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/std"
	"github.com/ichaly/ideabase/utl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetadataLoad_DatabaseAndConfig(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	k, err := std.NewKonfig()
	require.NoError(t, err)
	k.Set("mode", "dev")
	k.Set("app.root", utl.Root())
	k.Set("schema.schema", "public")
	k.Set("metadata.classes", map[string]*internal.ClassConfig{
		"UserAlias": {
			Table:       "users",
			Description: "用户别名视图",
			Fields: map[string]*internal.FieldConfig{
				"id": {
					Column:      "id",
					Type:        "int",
					Description: "用户ID",
					IsPrimary:   true,
				},
			},
		},
	})

	meta, err := NewMetadata(k, db)
	require.NoError(t, err)

	user, userExists := meta.Nodes["User"]
	userAlias, aliasExists := meta.Nodes["UserAlias"]
	usersTable, tableExists := meta.Nodes["users"]
	assert.True(t, userExists && aliasExists && tableExists, "User/UserAlias/users都应存在")
	assert.Same(t, user, usersTable, "User和users表名应为同指针")
	assert.NotSame(t, user, userAlias, "User和UserAlias应为不同指针")
	assert.Equal(t, user.Table, userAlias.Table, "User和UserAlias表名应一致")

	// 字段指针断言
	id1, ok1 := user.Fields["id"]
	id2, ok2 := userAlias.Fields["id"]
	assert.True(t, ok1 && ok2)
	assert.NotSame(t, id1, id2, "User和UserAlias的id字段应为不同指针")
}

func TestMetadataLoad_FileAndConfig(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	k, err := std.NewKonfig()
	require.NoError(t, err)
	k.Set("mode", "dev")
	k.Set("app.root", utl.Root())

	_, err = NewMetadata(k, db)
	require.NoError(t, err)

	k.Set("mode", "test")
	k.Set("metadata.file", "cfg/metadata.dev.json")
	k.Set("metadata.classes", map[string]*internal.ClassConfig{
		"PostAlias": {
			Table:       "posts",
			Description: "帖子别名视图",
			Fields: map[string]*internal.FieldConfig{
				"id": {
					Column:      "id",
					Type:        "int",
					Description: "帖子ID",
					IsPrimary:   true,
				},
			},
		},
	})

	meta2, err := NewMetadata(k, nil)
	require.NoError(t, err)

	post, postExists := meta2.Nodes["Post"]
	postAlias, aliasExists := meta2.Nodes["PostAlias"]
	postsTable, tableExists := meta2.Nodes["posts"]
	assert.True(t, postExists && aliasExists && tableExists, "Post/PostAlias/posts都应存在")
	assert.Same(t, post, postsTable, "Post和posts表名应为同指针")
	assert.NotSame(t, post, postAlias, "Post和PostAlias应为不同指针")
	assert.Equal(t, post.Table, postAlias.Table, "Post和PostAlias表名应一致")

	id1, ok1 := post.Fields["id"]
	id2, ok2 := postAlias.Fields["id"]
	assert.True(t, ok1 && ok2)
	assert.NotSame(t, id1, id2, "Post和PostAlias的id字段应为不同指针")
}

func TestMetadataLoad_ConfigOnly(t *testing.T) {
	k, err := std.NewKonfig()
	require.NoError(t, err)
	k.Set("mode", "test")
	k.Set("app.root", utl.Root())
	k.Set("metadata.classes", map[string]*internal.ClassConfig{
		"A": {
			Table:       "a_table",
			Description: "A类",
			Fields: map[string]*internal.FieldConfig{
				"id": {Column: "id", Type: "int", IsPrimary: true},
			},
		},
		"AAlias": {
			Table:       "a_table",
			Description: "A别名",
			Fields: map[string]*internal.FieldConfig{
				"id": {Column: "id", Type: "int", IsPrimary: true},
			},
		},
	})

	meta, err := NewMetadata(k, nil)
	require.NoError(t, err)
	a, aExists := meta.Nodes["A"]
	aAlias, aliasExists := meta.Nodes["AAlias"]

	aTable, tableExists := meta.Nodes["a_table"]
	assert.True(t, aExists && aliasExists && tableExists, "A/AAlias/a_table都应存在")
	assert.Same(t, a, aTable, "A和a_table表名应为同指针")
	assert.NotSame(t, a, aAlias, "A和AAlias应为不同指针")
	assert.Equal(t, a.Table, aAlias.Table, "A和AAlias表名应一致")

	id1, ok1 := a.Fields["id"]
	id2, ok2 := aAlias.Fields["id"]
	assert.True(t, ok1 && ok2)
	assert.NotSame(t, id1, id2, "A和AAlias的id字段应为不同指针")
}
