package gql

import (
	"fmt"
	"testing"
)

// 优化前的Write方法实现
func (my *Compiler) writeOld(list ...any) *Compiler {
	for _, e := range list {
		my.buf.WriteString(fmt.Sprint(e))
	}
	return my
}

func BenchmarkCompilerWrite(b *testing.B) {
	cases := []struct {
		name string
		data []any
	}{
		{
			name: "短字符串",
			data: []any{"a", "b", "c"},
		},
		{
			name: "长字符串",
			data: []any{"这是一个比较长的字符串用来测试性能", "another long string for testing performance"},
		},
		{
			name: "小数字",
			data: []any{1, 2, 3, 4, 5, 10, 20, 30, 40, 50},
		},
		{
			name: "大数字",
			data: []any{1234567, 7654321, 9999999},
		},
		{
			name: "浮点数",
			data: []any{1.23, 4.56, 7.89, 10.11, 12.13},
		},
		{
			name: "整数浮点",
			data: []any{1.0, 2.0, 3.0, 4.0, 5.0},
		},
		{
			name: "布尔值",
			data: []any{true, false, true, false, true},
		},
		{
			name: "混合类型",
			data: []any{"test", 123, true, 45.67, "end"},
		},
		{
			name: "大量小数字",
			data: func() []any {
				data := make([]any, 100)
				for i := 0; i < 100; i++ {
					data[i] = i
				}
				return data
			}(),
		},
		{
			name: "大量混合",
			data: func() []any {
				data := make([]any, 100)
				for i := 0; i < 100; i++ {
					switch i % 4 {
					case 0:
						data[i] = i
					case 1:
						data[i] = fmt.Sprintf("str%d", i)
					case 2:
						data[i] = float64(i) + 0.5
					case 3:
						data[i] = i%2 == 0
					}
				}
				return data
			}(),
		},
	}

	for _, tc := range cases {
		b.Run("Old_"+tc.name, func(b *testing.B) {
			cpl := NewCompiler(nil, nil)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				cpl.buf.Reset()
				cpl.writeOld(tc.data...)
			}
		})

		b.Run("New_"+tc.name, func(b *testing.B) {
			cpl := NewCompiler(nil, nil)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				cpl.buf.Reset()
				cpl.Write(tc.data...)
			}
		})
	}
}
