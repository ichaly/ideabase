package renderer

import "strings"

// build 渲染单个字段定义文本
func build(f *Field) string {
	var sb strings.Builder
	indent := func(n int) {
		for i := 0; i < n; i++ {
			sb.WriteByte(' ')
		}
	}
	writeType := func() {
		if f.Type.IsList {
			sb.WriteByte('[')
			sb.WriteString(f.Type.Name)
			if f.Type.ListItemNonNull {
				sb.WriteByte('!')
			}
			sb.WriteByte(']')
		} else {
			sb.WriteString(f.Type.Name)
		}
		if f.Type.IsNonNull {
			sb.WriteByte('!')
		}
	}
	tail := func() {
		sb.WriteString(": ")
		writeType()
		if f.Comment != "" {
			sb.WriteString("  # ")
			sb.WriteString(f.Comment)
		}
	}

	indent(f.Indent)
	sb.WriteString(f.Name)
	switch {
	case len(f.Args) > 0 && f.Multiline: // 多行参数：名(\n  参数行...\n): 类型
		sb.WriteString("(\n")
		for i, arg := range f.Args {
			indent(f.Indent + 2)
			sb.WriteString(arg.Name)
			sb.WriteString(": ")
			sb.WriteString(arg.Type)
			if i < len(f.Args)-1 {
				sb.WriteByte('\n')
			}
		}
		sb.WriteByte('\n')
		indent(f.Indent)
		sb.WriteByte(')')
		tail()
	case len(f.Args) > 0: // 内联参数
		sb.WriteByte('(')
		for i, arg := range f.Args {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(arg.Name)
			sb.WriteString(": ")
			sb.WriteString(arg.Type)
		}
		sb.WriteByte(')')
		tail()
	default:
		tail()
	}
	return sb.String()
}

// MakeField 创建并渲染字段定义的便捷方法
func MakeField(name string, typeName string, options ...Option) string {
	f := &Field{Name: name, Indent: 2}
	f.Type.Name = typeName
	for _, opt := range options {
		opt(f)
	}
	return build(f)
}
