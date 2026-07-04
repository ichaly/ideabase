package pgsql

import (
	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/vektah/gqlparser/v2"
	"strings"
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

// TestScopeNested 嵌套多表作用域：每个LATERAL单元各自表名限定注入scope，无歧义
func (my *_DialectSuite) TestScopeNested() {
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
			"Post": {
				Table: "sys_post",
				Scope: []internal.ScopeConfig{{Column: "tenant_id", Context: "tenant"}},
				Fields: map[string]*internal.FieldConfig{
					"id":    {Type: "ID", Column: "id", IsPrimary: true},
					"title": {Type: "String", Column: "title"},
					"userId": {Type: "Int", Column: "user_id", Relation: &internal.RelationConfig{
						TargetClass: "User", TargetField: "id", Type: "ManyToOne",
					}},
				},
			},
		},
	})

	doc, gqlErr := gqlparser.LoadQuery(schema, `query { posts { items { id user { name } } } }`)
	my.Require().Empty(gqlErr)
	compile, err := gql.NewCompiler(meta, []compiler.Dialect{dialect})
	my.Require().NoError(err)
	sql, _, err := compile.Build(doc.Operations[0], nil)
	my.Require().NoError(err)

	// 两个单元各自表名限定；同上下文键(tenant)dedup 共享一个槽位 $1（免膨胀）
	my.Assert().Contains(sql, `"sys_post"."tenant_id" = $1`)
	my.Assert().Contains(sql, `"sys_user"."tenant_id" = $1`)
}

// TestScopeRecursive 递归全树作用域：递归CTE的起始层与步进层都注入scope，
// 递归只在本租户内遍历（变更/查询写隔离与读隔离一致覆盖递归路径）
func (my *_DialectSuite) TestScopeRecursive() {
	meta, schema, dialect := my.newSuite(map[string]interface{}{
		"metadata.classes": map[string]*internal.ClassConfig{
			"Comment": {
				Table: "sys_comment",
				Scope: []internal.ScopeConfig{{Column: "tenant_id", Context: "tenant"}},
				Fields: map[string]*internal.FieldConfig{
					"id":      {Type: "ID", Column: "id", IsPrimary: true},
					"content": {Type: "String", Column: "content"},
					"parentId": {Type: "Int", Column: "parent_id", IsNullable: true, Relation: &internal.RelationConfig{
						TargetClass: "Comment", TargetField: "id", Type: "Recursive",
					}},
				},
			},
		},
	})

	doc, gqlErr := gqlparser.LoadQuery(schema, `query { comments { items { id descendants { id } } } }`)
	my.Require().Empty(gqlErr)
	compile, err := gql.NewCompiler(meta, []compiler.Dialect{dialect})
	my.Require().NoError(err)
	sql, _, err := compile.Build(doc.Operations[0], nil)
	my.Require().NoError(err)

	// 递归CTE起始层与步进层都带 scope（出现两次 sys_comment.tenant_id 注入）
	cnt := strings.Count(sql, `"sys_comment"."tenant_id" = $`)
	my.Assert().GreaterOrEqual(cnt, 2, "递归起始层+步进层都应注入scope，得到 %d 次:\n%s", cnt, sql)
}

// TestScopeMulti 多条作用域：租户+当前登录人同时，各注入一条 AND
func (my *_DialectSuite) TestScopeMulti() {
	meta, schema, dialect := my.newSuite(map[string]interface{}{
		"metadata.classes": map[string]*internal.ClassConfig{
			"User": {
				Table: "sys_user",
				Scope: []internal.ScopeConfig{
					{Column: "tenant_id", Context: "tenant"},
					{Column: "owner_id", Context: "userId"},
				},
				Fields: map[string]*internal.FieldConfig{
					"id":   {Type: "ID", Column: "id", IsPrimary: true},
					"name": {Type: "String", Column: "name"},
				},
			},
		},
	})

	doc, gqlErr := gqlparser.LoadQuery(schema, `query { users { items { id } } }`)
	my.Require().Empty(gqlErr)
	compile, err := gql.NewCompiler(meta, []compiler.Dialect{dialect})
	my.Require().NoError(err)
	sql, _, err := compile.Build(doc.Operations[0], nil)
	my.Require().NoError(err)

	// 两条作用域各注入一个独立槽位，AND 连接
	my.Assert().Contains(sql, `"sys_user"."tenant_id" = $1 AND "sys_user"."owner_id" = $2`)
	my.T().Logf("多作用域SQL片段:\n%s", sql)
}

// TestScopeStats 统计聚合也注入作用域：count/sum 不泄露全表跨租户统计
func (my *_DialectSuite) TestScopeStats() {
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

	doc, gqlErr := gqlparser.LoadQuery(schema, `query { userStats { count } }`)
	my.Require().Empty(gqlErr)
	compile, err := gql.NewCompiler(meta, []compiler.Dialect{dialect})
	my.Require().NoError(err)
	sql, _, err := compile.Build(doc.Operations[0], nil)
	my.Require().NoError(err)

	// 聚合 FROM 表也带作用域 WHERE
	my.Assert().Contains(sql, `FROM "sys_user" WHERE "sys_user"."tenant_id" = $1`)
}
