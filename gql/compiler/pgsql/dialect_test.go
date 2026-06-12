package pgsql

import (
	"strings"
	"testing"

	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/suite"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// Case 端到端编译用例：GraphQL查询 -> 期望SQL(+参数)
type Case struct {
	name      string
	query     string
	variables map[string]interface{}
	expected  string
	args      []any
}

type _DialectSuite struct {
	suite.Suite
	meta    *gql.Metadata
	schema  *ast.Schema
	dialect *Dialect
}

func TestDialect(t *testing.T) {
	suite.Run(t, new(_DialectSuite))
}

// SetupSuite 构造覆盖四种关系的测试元数据：
// User 1-N Post N-N Tag（经sys_post_tag），Comment 自关联递归
func (my *_DialectSuite) SetupSuite() {
	k, err := std.NewKonfig()
	my.Require().NoError(err, "创建配置失败")
	k.Set("mode", "dev")
	k.Set("app.root", my.T().TempDir())
	k.Set("metadata.table-prefix", []string{"sys_"})

	k.Set("metadata.classes", map[string]*internal.ClassConfig{
		"User": {
			Table: "sys_user",
			Fields: map[string]*internal.FieldConfig{
				"id":    {Type: "ID", Column: "id", IsPrimary: true},
				"name":  {Type: "String", Column: "name"},
				"email": {Type: "String", Column: "email"},
				"age":   {Type: "Int", Column: "age", IsNullable: true},
			},
		},
		"Post": {
			Table: "sys_post",
			Fields: map[string]*internal.FieldConfig{
				"id": {Type: "ID", Column: "id", IsPrimary: true, Relation: &internal.RelationConfig{
					// 多对多：Post经sys_post_tag关联Tag
					TargetClass: "Tag", TargetField: "id", Type: "ManyToMany",
					Through: &internal.ThroughConfig{TableName: "sys_post_tag", SourceKey: "post_id", TargetKey: "tag_id"},
				}},
				"title": {Type: "String", Column: "title"},
				"userId": {Type: "Int", Column: "user_id", Relation: &internal.RelationConfig{
					TargetClass: "User", TargetField: "id", Type: "ManyToOne",
				}},
			},
		},
		"Tag": {
			Table: "sys_tag",
			Fields: map[string]*internal.FieldConfig{
				"id":   {Type: "ID", Column: "id", IsPrimary: true},
				"name": {Type: "String", Column: "name"},
			},
		},
		"Comment": {
			Table: "sys_comment",
			Fields: map[string]*internal.FieldConfig{
				"id":      {Type: "ID", Column: "id", IsPrimary: true},
				"content": {Type: "String", Column: "content"},
				"parentId": {Type: "Int", Column: "parent_id", IsNullable: true, Relation: &internal.RelationConfig{
					TargetClass: "Comment", TargetField: "id", Type: "Recursive",
				}},
			},
		},
	})

	meta, err := gql.NewMetadata(k, nil)
	my.Require().NoError(err, "创建元数据失败")
	my.meta = meta
	my.dialect = &Dialect{}

	schemaStr, err := gql.NewRenderer(meta).Generate()
	my.Require().NoError(err, "生成GraphQL schema失败")

	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "schema-test.graphql", Input: schemaStr})
	my.Require().NoError(err, "加载GraphQL schema失败")
	my.schema = schema
}

func (my *_DialectSuite) runCases(cases []Case) {
	for _, c := range cases {
		my.Run(c.name, func() {
			doc, gqlErr := gqlparser.LoadQuery(my.schema, c.query)
			my.Require().Empty(gqlErr, "解析GraphQL查询失败")
			my.Require().NotEmpty(doc.Operations, "GraphQL查询必须包含操作")

			compile, err := gql.NewCompiler(my.meta, []compiler.Dialect{my.dialect})
			my.Require().NoError(err, "创建编译器失败")

			sql, args, err := compile.Build(doc.Operations[0], c.variables)
			my.Require().NoError(err, "编译GraphQL查询失败")

			my.Assert().Equal(normalizeSQL(c.expected), normalizeSQL(sql), "生成的SQL与预期不符")
			if c.args != nil {
				my.Assert().Equal(c.args, args, "SQL参数与预期不符")
			}
		})
	}
}

// normalizeSQL 空白归一化（含括号邻接空格），比较时忽略格式差异
func normalizeSQL(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.ReplaceAll(s, "( ", "(")
	s = strings.ReplaceAll(s, " )", ")")
	return s
}
