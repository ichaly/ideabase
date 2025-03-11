package field

import (
	"strings"
	"sync"
)

// StringBuilder 高性能字符串构建器
type StringBuilder struct {
	builder strings.Builder
}

// Reset 重置构建器
func (sb *StringBuilder) Reset() {
	sb.builder.Reset()
}

// Grow 预分配容量
func (sb *StringBuilder) Grow(n int) {
	sb.builder.Grow(n)
}

// WriteString 写入字符串
func (sb *StringBuilder) WriteString(s string) {
	sb.builder.WriteString(s)
}

// WriteByte 写入字节
func (sb *StringBuilder) WriteByte(b byte) {
	sb.builder.WriteByte(b)
}

// WriteIndent 写入指定数量的空格
func (sb *StringBuilder) WriteIndent(n int) {
	for i := 0; i < n; i++ {
		sb.builder.WriteByte(' ')
	}
}

// String 获取最终字符串
func (sb *StringBuilder) String() string {
	return sb.builder.String()
}

// Builder 字段构建器
type Builder struct {
	sb StringBuilder
}

// NewBuilder 创建新的字段构建器
func NewBuilder() *Builder {
	return &Builder{}
}

// Build 构建字段定义字符串
func (b *Builder) Build(f *Field) string {
	// 预估容量减少内存分配
	b.sb.Reset()
	b.sb.Grow(estimateSize(f))

	// 添加缩进
	b.sb.WriteIndent(f.Indent)

	// 字段名
	b.sb.WriteString(f.Name)

	// 参数处理
	if len(f.Args) > 0 {
		if f.Multiline {
			// 多行参数 - 直接返回第一行，参数另起行
			b.sb.WriteString("(")
			return b.finishField(f, true)
		} else {
			// 内联参数
			b.sb.WriteByte('(')
			for i, arg := range f.Args {
				if i > 0 {
					b.sb.WriteString(", ")
				}

				b.sb.WriteString(arg.Name)
				b.sb.WriteString(": ")
				b.sb.WriteString(arg.Type)
			}
			b.sb.WriteByte(')')
		}
	}

	return b.finishField(f, false)
}

// finishField 完成字段的构建
func (b *Builder) finishField(f *Field, isMultiline bool) string {
	// 如果是多行参数，直接返回当前内容，后续内容另起一行
	if isMultiline {
		// 获取第一行（字段名和左括号）
		firstLine := b.sb.String()

		// 重置构建器用于构建参数行
		b.sb.Reset()

		// 添加参数行
		for i, arg := range f.Args {
			// 参数行缩进
			b.sb.WriteIndent(f.Indent + 2)
			b.sb.WriteString(arg.Name)
			b.sb.WriteString(": ")
			b.sb.WriteString(arg.Type)

			// 不是最后一个参数，添加换行
			if i < len(f.Args)-1 {
				b.sb.WriteByte('\n')
			}
		}

		// 保存参数行
		paramsBlock := b.sb.String()

		// 重置构建器用于构建最后一行 (右括号和类型)
		b.sb.Reset()
		b.sb.WriteIndent(f.Indent)
		b.sb.WriteByte(')')

		// 类型信息
		b.sb.WriteString(": ")
		b.writeType(f)

		// 添加注释
		if f.Comment != "" {
			b.sb.WriteString("  # ")
			b.sb.WriteString(f.Comment)
		}

		// 返回三部分内容
		return firstLine + "\n" + paramsBlock + "\n" + b.sb.String()
	}

	// 单行形式
	b.sb.WriteString(": ")
	b.writeType(f)

	// 添加注释
	if f.Comment != "" {
		b.sb.WriteString("  # ")
		b.sb.WriteString(f.Comment)
	}

	return b.sb.String()
}

// writeType 写入类型定义
func (b *Builder) writeType(f *Field) {
	if f.Type.IsList {
		b.sb.WriteByte('[')
		b.sb.WriteString(f.Type.Name)
		if f.Type.ListItemNonNull {
			b.sb.WriteByte('!')
		}
		b.sb.WriteByte(']')
	} else {
		b.sb.WriteString(f.Type.Name)
	}

	if f.Type.IsNonNull {
		b.sb.WriteByte('!')
	}
}

// 预估字段字符串长度
func estimateSize(f *Field) int {
	size := f.Indent + len(f.Name) + len(f.Type.Name) + 4 // 基础大小

	if f.Type.IsList {
		size += 2 // 添加 []
	}

	if f.Type.IsNonNull || f.Type.ListItemNonNull {
		size += 1 // 添加 !
	}

	if f.Comment != "" {
		size += len(f.Comment) + 4 // 添加注释和分隔符
	}

	if len(f.Args) > 0 {
		size += 2 // 添加 ()
		for _, arg := range f.Args {
			size += len(arg.Name) + len(arg.Type) + 4 // 名称、类型和分隔符
		}

		if f.Multiline && len(f.Args) > 1 {
			size += len(f.Args) * 2 // 每个参数的换行和额外缩进
		}
	}

	return size
}

// 全局字段构建器
var globalBuilder = NewBuilder()
var builderMutex sync.Mutex

// BuildField 使用全局构建器生成字段字符串
func BuildField(f *Field) string {
	builderMutex.Lock()
	defer builderMutex.Unlock()
	return globalBuilder.Build(f)
}

// MakeField 创建、构建并释放字段的便捷方法
func MakeField(name string, typeName string, options ...Option) string {
	f := New(name, typeName, options...)
	str := BuildField(f)
	Release(f)
	return str
}
