package gql

import (
	"context"
	"reflect"
	"testing"
)

// 只测输入绑定、规范化和校验，不把数据库延迟混入参数处理的测量。
func BenchmarkMutationBinding(b *testing.B) {
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
	m := NewMutation[Input]("Meter")
	plan := &mutationInput{index: make(map[string]int)}
	eachField(reflect.TypeFor[Input](), func(f reflect.StructField, name, _ string) {
		plan.index[name] = len(plan.fields)
		plan.fields = append(plan.fields, newMutationInputField(f, name, true))
	})
	full := map[string]any{"name": " meter ", "count": 10, "active": false, "amount": "9007199254740993", "price": "0", "ratio": 1.25, "root": "/", "extra": nil}
	batch := make([]any, 100)
	for i := range batch {
		batch[i] = full
	}
	for _, tc := range []struct {
		name, operation string
		input           any
	}{
		{"create", "create", full},
		{"patch", "update", map[string]any{"name": " next ", "amount": "9007199254740993"}},
		{"batch100", "create", batch},
	} {
		b.Run(tc.name, func(b *testing.B) {
			ctx := context.Background()
			args := map[string]any{"input": tc.input}
			b.ReportAllocs()
			for b.Loop() {
				request := MutationRequest{tc.operation, cloneVariableValue(args).(map[string]any)}
				rows, err := mutationRows(request.Arguments)
				if err != nil {
					b.Fatal(err)
				}
				if err := m.bind(ctx, request, rows, plan); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
