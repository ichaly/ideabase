package gql

import (
	"context"
	"reflect"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// 测完整参数绑定、PATCH字段选择、校验与写回，不包含数据库延迟。
func BenchmarkNativeBinding(b *testing.B) {
	type Input struct {
		Name   string `validate:"required,max=100"`
		Count  int    `validate:"min=0,max=1000"`
		Active bool
		Amount int64   `validate:"min=0"`
		Price  *int64  `validate:"omitempty,min=0"`
		Ratio  float64 `validate:"min=0"`
		Root   string
		Extra  *string
	}
	type Args struct{ Input Input }
	type Batch struct {
		Input []Input `validate:"dive"`
	}
	schema := gqlparser.MustLoadSchema(&ast.Source{Input: `scalar BigInt
input MeterInput {name:String count:Int active:Boolean amount:BigInt price:BigInt ratio:Float root:String extra:String}
type Query{x:Int}`})
	e := &Engine{schema: schema}
	v, err := validator()
	if err != nil {
		b.Fatal(err)
	}
	full := map[string]any{"name": " meter ", "count": 10, "active": false, "amount": "9007199254740993", "price": "0", "ratio": 1.25, "root": "/", "extra": nil}
	batch := make([]any, 100)
	for i := range batch {
		batch[i] = full
	}
	for _, tc := range []struct {
		name    string
		t       reflect.Type
		typ     *ast.Type
		input   any
		partial bool
	}{
		{"create", reflect.TypeFor[Args](), ast.NamedType("MeterInput", nil), full, false},
		{"patch", reflect.TypeFor[Args](), ast.NamedType("MeterInput", nil), map[string]any{"name": " next ", "amount": "9007199254740993"}, true},
		{"batch100", reflect.TypeFor[Batch](), ast.ListType(ast.NamedType("MeterInput", nil), nil), batch, false},
	} {
		b.Run(tc.name, func(b *testing.B) {
			plan, err := e.bindInput(tc.t, nil, ast.FieldList{&ast.FieldDefinition{Name: "input", Type: tc.typ}})
			if err != nil {
				b.Fatal(err)
			}
			args := map[string]any{"input": tc.input}
			ctx := context.Background()
			b.ReportAllocs()
			for b.Loop() {
				raw := cloneVariableValue(args)
				value := reflect.New(tc.t)
				state := inputState{}
				if err := plan.decode(value.Elem(), raw, &state); err != nil {
					b.Fatal(err)
				}
				var selected []string
				if tc.partial {
					plan.selectFields(value.Elem(), "", &state, &selected)
					err = v.StructPartialCtx(ctx, value.Interface(), selected...)
				} else {
					err = v.StructCtx(ctx, value.Interface())
				}
				if err != nil {
					b.Fatal(err)
				}
				if _, err := plan.encode(value.Elem(), raw, &state); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
