package pgsql

import (
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
)

var compatSQLSink string
var compatArgsSink []any
var compatErrSink error

// These contract assertions accompany the warm compile corpus. They avoid
// ns/op: SQL shape and argument slots are reproducible; timings are not.
func BenchmarkCompatCompileBaselineSimple(b *testing.B) {
	compile, schema := benchCompiler(b)
	op := benchOperation(b, schema, `{ posts(limit: 10) { items { id title } total } }`)
	assertBaseline(b, compile, op, false)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		compatSQLSink, compatArgsSink, compatErrSink = compile.Build(op, nil)
	}
	b.StopTimer()
	assertBenchmarkResult(b, false)
}

func BenchmarkCompatCompileBaselineNested(b *testing.B) {
	compile, schema := benchCompiler(b)
	op := benchOperation(b, schema, `{ posts(limit: 10) { items { id title user { name } tags { name } } total } }`)
	assertBaseline(b, compile, op, true)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		compatSQLSink, compatArgsSink, compatErrSink = compile.Build(op, nil)
	}
	b.StopTimer()
	assertBenchmarkResult(b, true)
}

func assertBaseline(b *testing.B, compile interface {
	Build(*ast.OperationDefinition, map[string]interface{}) (string, []any, error)
}, op *ast.OperationDefinition, nested bool) {
	sql, args, err := compile.Build(op, nil)
	if err != nil {
		b.Fatal(err)
	}
	if sql == "" {
		b.Fatal("compile baseline produced empty SQL")
	}
	if len(args) != 0 {
		b.Fatalf("baseline args = %d, want 0: %#v", len(args), args)
	}
	if nested {
		assertNestedSQL(b, sql)
	}
}

func assertBenchmarkResult(b *testing.B, nested bool) {
	if compatErrSink != nil || compatSQLSink == "" || len(compatArgsSink) != 0 {
		b.Fatalf("benchmark result contract violated: sql=%q args=%#v", compatSQLSink, compatArgsSink)
	}
	if nested {
		assertNestedSQL(b, compatSQLSink)
	}
}

func assertNestedSQL(b *testing.B, sql string) {
	for _, fragment := range []string{`LATERAL`, `sys_user`, `sys_tag`} {
		for i := 0; i < len(sql); i++ { // avoid importing/doing checks in the timed loop
			if i+len(fragment) <= len(sql) && sql[i:i+len(fragment)] == fragment {
				goto found
			}
		}
		b.Fatalf("nested baseline SQL missing %q", fragment)
	found:
	}
}
