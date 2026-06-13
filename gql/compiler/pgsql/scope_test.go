package pgsql

import (
	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/vektah/gqlparser/v2"
)

// TestScope 行级作用域：实体声明scope时编译期强制注入 列=上下文值，
// 与用户where一起AND；scope列是底层强制，不作为可查字段进schema
func (my *_DialectSuite) TestScope() {
	meta, schema, dialect := my.newSuite(map[string]interface{}{
		"metadata.classes": map[string]*internal.ClassConfig{
			"User": {
				Table: "sys_user",
				Scope: []internal.ScopeConfig{{Column: "tenant_id", Context: "tenant"}},
				Fields: map[string]*internal.FieldConfig{
					"id":   {Type: "ID", Column: "id", IsPrimary: true},
					"name": {Type: "String", Column: "name"},
				},
			},
		},
	})

	doc, gqlErr := gqlparser.LoadQuery(schema, `query { users(where: { name: { eq: "x" } }) { items { id } } }`)
	my.Require().Empty(gqlErr)
	compile, err := gql.NewCompiler(meta, []compiler.Dialect{dialect})
	my.Require().NoError(err)
	sql, args, err := compile.Build(doc.Operations[0], nil)
	my.Require().NoError(err)

	// scope 与用户 where 一起 AND；scope 列直接强制注入
	my.Assert().Contains(sql, `WHERE "sys_user"."tenant_id" = $1 AND "sys_user"."name" = $2`)
	// scope 槽位执行期从上下文取值（Build 未传作用域则为 nil）
	my.Assert().Equal([]any{nil, "x"}, args)
	// tenant_id 不作为可查字段进 schema（schema=能力=自省 仍一致）
	_, ok := my.meta.GetNode("User")
	my.Require().True(ok)
	my.Assert().NotContains(sql, `tenantId`)
}
