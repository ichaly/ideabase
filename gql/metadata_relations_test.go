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
	schema, err := NewRenderer(meta).Render()
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

type mismatchedRelationLoader struct{}

func (mismatchedRelationLoader) Name() string  { return "mismatched-relation" }
func (mismatchedRelationLoader) Priority() int { return 0 }
func (mismatchedRelationLoader) Support() bool { return true }
func (mismatchedRelationLoader) Load(h protocol.Hoster) error {
	relation := &protocol.Relation{
		Type: protocol.MANY_TO_ONE, SourceClass: "Order", TargetClass: "Customer",
		SourceField: "customerId", SourceFields: []string{"customerId", "regionId"},
		TargetField: "id", TargetFields: []string{"id"},
	}
	if err := h.PutNode("Customer", &protocol.Class{Name: "Customer", Table: "customers", Fields: map[string]*protocol.Field{
		"id": {Name: "id", Column: "id", Type: "ID"},
	}}); err != nil {
		return err
	}
	return h.PutNode("Order", &protocol.Class{Name: "Order", Table: "orders", Relations: []*protocol.Relation{relation}, Fields: map[string]*protocol.Field{
		"customerId": {Name: "customerId", Column: "customer_id", Type: "ID"},
		"regionId":   {Name: "regionId", Column: "region_id", Type: "ID"},
	}})
}

type nilRelationLoader struct{ mismatchedRelationLoader }

func (nilRelationLoader) Name() string { return "nil-relation" }
func (nilRelationLoader) Load(h protocol.Hoster) error {
	return h.PutNode("Broken", &protocol.Class{Name: "Broken", Table: "broken", Relations: []*protocol.Relation{nil}, Fields: map[string]*protocol.Field{}})
}

func TestMetadataRejectsMismatchedCompositeRelation(t *testing.T) {
	_, err := NewMetadata(failClosedKonfig(t), nil,
		WithoutLoader("pgsql", "mysql", "file", "config"),
		WithLoader(mismatchedRelationLoader{}),
	)
	require.ErrorContains(t, err, "source_fields 与 target_fields 必须非空且等长")
}

func TestMetadataRejectsNilRelation(t *testing.T) {
	_, err := NewMetadata(failClosedKonfig(t), nil,
		WithoutLoader("pgsql", "mysql", "file", "config"),
		WithLoader(nilRelationLoader{}),
	)
	require.ErrorContains(t, err, "包含空关系")
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
