package pgsql

import (
	"testing"

	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/std"
	"github.com/rs/zerolog"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func init() { zerolog.SetGlobalLevel(zerolog.Disabled) } // 基准跑期静音schema生成日志

// 纯引擎编译基准：不连数据库（NewMetadata(k, nil)），测 GraphQL→SQL 编译的
// CPU 与分配——每个计划缓存未命中的请求都走此路径，是引擎核心热路径。
// 运行：go test -bench=BenchmarkCompile -benchmem ./compiler/pgsql/

func benchCompiler(b *testing.B) (*gql.Compiler, *ast.Schema) {
	k, err := std.NewKonfig()
	if err != nil {
		b.Fatal(err)
	}
	k.Set("mode", "dev")
	k.Set("app.root", b.TempDir())
	k.Set("metadata.table-prefix", []string{"sys_"})
	k.Set("metadata.classes", map[string]*internal.ClassConfig{
		"User": {
			Table: "sys_user",
			Fields: map[string]*internal.FieldConfig{
				"id":   {Type: "ID", Column: "id", IsPrimary: true},
				"name": {Type: "String", Column: "name"},
			},
		},
		"Post": {
			Table: "sys_post",
			Fields: map[string]*internal.FieldConfig{
				"id":    {Type: "ID", Column: "id", IsPrimary: true},
				"title": {Type: "String", Column: "title"},
				"userId": {Type: "Int", Column: "user_id", Relation: &internal.RelationConfig{
					TargetClass: "User", TargetField: "id", Type: "ManyToOne",
				}},
				"tagId": {Type: "Int", Column: "tag_id", Relation: &internal.RelationConfig{
					TargetClass: "Tag", TargetField: "id", Type: "ManyToMany",
					Through: &internal.ThroughConfig{TableName: "sys_post_tag", SourceKey: "post_id", TargetKey: "tag_id"},
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
	})

	meta, err := gql.NewMetadata(k, nil)
	if err != nil {
		b.Fatal(err)
	}
	compile, err := gql.NewCompiler(meta, []compiler.Dialect{&Dialect{}})
	if err != nil {
		b.Fatal(err)
	}
	schemaStr, err := gql.NewRenderer(meta).Generate()
	if err != nil {
		b.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "bench.graphql", Input: schemaStr})
	if err != nil {
		b.Fatal(err)
	}
	return compile, schema
}

func benchOperation(b *testing.B, schema *ast.Schema, query string) *ast.OperationDefinition {
	doc, errs := gqlparser.LoadQuery(schema, query)
	if len(errs) > 0 {
		b.Fatal(errs)
	}
	return doc.Operations[0]
}

// BenchmarkCompileSimple 单表查询编译（无关系）
func BenchmarkCompileSimple(b *testing.B) {
	compile, schema := benchCompiler(b)
	op := benchOperation(b, schema, `{ posts(limit: 10) { items { id title } total } }`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := compile.Build(op, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCompileNested 嵌套关系查询编译（多对一 user + 多对多 tags，生成 LATERAL JOIN）
func BenchmarkCompileNested(b *testing.B) {
	compile, schema := benchCompiler(b)
	op := benchOperation(b, schema, `{ posts(limit: 10) { items { id title user { name } tags { name } } total } }`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := compile.Build(op, nil); err != nil {
			b.Fatal(err)
		}
	}
}
