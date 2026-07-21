package gql

import (
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// relMeta 构造配置驱动的元数据（关系集合测试共用）
func relMeta(t *testing.T, classes map[string]*internal.ClassConfig) *Metadata {
	k, err := std.NewKonfig()
	require.NoError(t, err)
	k.Set("mode", "dev")
	k.Set("app.root", t.TempDir())
	k.Set("metadata.classes", classes)
	meta, err := NewMetadata(k, nil)
	require.NoError(t, err)
	return meta
}

// 同两类间多条外键:正向字段按源列词干命名,反向列表逐关系生成(单指针模型
// 只会产出 user/user1 + 单个 comments,第二条反向入口静默丢失)
func TestRelationsSameTargetMultiFK(t *testing.T) {
	meta := relMeta(t, map[string]*internal.ClassConfig{
		"User": {
			Table: "users",
			Fields: map[string]*internal.FieldConfig{
				"id":   {Type: "ID", Column: "id", IsPrimary: true},
				"name": {Type: "String", Column: "name"},
			},
		},
		"Comment": {
			Table: "comments",
			Fields: map[string]*internal.FieldConfig{
				"id":      {Type: "ID", Column: "id", IsPrimary: true},
				"content": {Type: "String", Column: "content"},
				"authorId": {Type: "Int", Column: "author_id", Relation: &internal.RelationConfig{
					TargetClass: "User", TargetField: "id", Type: "ManyToOne",
				}},
				"editorId": {Type: "Int", Column: "editor_id", IsNullable: true, Relation: &internal.RelationConfig{
					TargetClass: "User", TargetField: "id", Type: "ManyToOne",
				}},
			},
		},
	})

	comment := meta.Nodes["Comment"]
	require.NotNil(t, comment)
	// 正向:词干命名,不再是 user/user1
	author, editor := comment.Fields["author"], comment.Fields["editor"]
	require.NotNil(t, author, "authorId应生成author字段, 实际字段: %v", fieldNamesOf(comment))
	require.NotNil(t, editor, "editorId应生成editor字段")
	// config通道SourceField为字段名（消费方经scope.column解析为列名）
	assert.Equal(t, "authorId", author.Relation.SourceField)
	assert.Equal(t, "editorId", editor.Relation.SourceField)
	assert.Nil(t, comment.Fields["user1"], "不应出现顺序后缀幽灵字段")

	// 反向:两条列表都在,按源列词干区分
	user := meta.Nodes["User"]
	require.NotNil(t, user.Fields["authorComments"], "author反向列表应存在, 实际字段: %v", fieldNamesOf(user))
	require.NotNil(t, user.Fields["editorComments"], "editor反向列表应存在")
	assert.Nil(t, user.Fields["comments1"], "不应出现顺序后缀幽灵字段")

	// schema 可加载
	schema, err := NewRenderer(meta).Generate()
	require.NoError(t, err)
	_, gqlErr := gqlparser.LoadSchema(&ast.Source{Name: "multi-fk.graphql", Input: schema})
	require.Nil(t, gqlErr, "schema应可加载")
}

// 单关系场景命名与现状完全一致(兼容零漂移)
func TestRelationsSingleFKNamingUnchanged(t *testing.T) {
	meta := relMeta(t, map[string]*internal.ClassConfig{
		"User": {
			Table: "users",
			Fields: map[string]*internal.FieldConfig{
				"id": {Type: "ID", Column: "id", IsPrimary: true},
			},
		},
		"Post": {
			Table: "posts",
			Fields: map[string]*internal.FieldConfig{
				"id": {Type: "ID", Column: "id", IsPrimary: true},
				"userId": {Type: "Int", Column: "user_id", Relation: &internal.RelationConfig{
					TargetClass: "User", TargetField: "id", Type: "ManyToOne",
				}},
			},
		},
	})
	require.NotNil(t, meta.Nodes["Post"].Fields["user"], "单关系正向保持user")
	require.NotNil(t, meta.Nodes["User"].Fields["posts"], "单关系反向保持posts")
}

func fieldNamesOf(class *protocol.Class) []string {
	var names []string
	for key, f := range class.Fields {
		if key == f.Name {
			names = append(names, key)
		}
	}
	return names
}
