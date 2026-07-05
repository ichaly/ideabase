package gql

import (
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// 不可数类名（Series/News等）复数同形：批量create与单条create重名会让
// gqlparser加载schema直接失败——整个服务起不来，而非单表问题
func TestRenderer_UncountableClassName(t *testing.T) {
	k, err := std.NewKonfig()
	require.NoError(t, err)
	k.Set("mode", "dev")
	k.Set("app.root", t.TempDir())
	k.Set("metadata.classes", map[string]*internal.ClassConfig{
		"Series": {
			Table: "series",
			Fields: map[string]*internal.FieldConfig{
				"id":    {Type: "ID", Column: "id", IsPrimary: true},
				"title": {Type: "String", Column: "title"},
			},
		},
	})

	meta, err := NewMetadata(k, nil)
	require.NoError(t, err)
	schema, err := NewRenderer(meta).Generate()
	require.NoError(t, err)

	_, gqlErr := gqlparser.LoadSchema(&ast.Source{Name: "uncountable.graphql", Input: schema})
	require.Nil(t, gqlErr, "复数同形类名生成的schema必须可加载")

	// 单条create/update/delete与upsert保留；批量create因与单条重名而不渲染
	assert.Contains(t, schema, "createSeries(input: SeriesCreateInput!)", "单条创建保留")
	assert.Contains(t, schema, "upsertSeries", "upsert只有复数形态,无冲突,保留")
	assert.NotContains(t, schema, "createSeries(input: [SeriesCreateInput!]!)", "批量创建与单条重名,不渲染")
}
