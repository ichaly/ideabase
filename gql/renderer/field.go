package renderer

// Type 表示字段类型
type Type struct {
	Name            string
	IsNonNull       bool
	IsList          bool
	ListItemNonNull bool
}

// Field 表示GraphQL字段
type Field struct {
	Name      string
	Type      Type
	Comment   string
	Args      []Argument
	Indent    int
	Multiline bool
}

// Argument 表示字段参数
type Argument struct {
	Name string
	Type string
}

// Option 配置字段的函数选项
type Option func(*Field)

// NonNull 标记字段为非空
func NonNull() Option {
	return func(f *Field) {
		f.Type.IsNonNull = true
	}
}

// List 标记字段为列表类型
func List() Option {
	return func(f *Field) {
		f.Type.IsList = true
	}
}

// ListNonNull 标记列表元素为非空
func ListNonNull() Option {
	return func(f *Field) {
		f.Type.IsList = true
		f.Type.ListItemNonNull = true
	}
}

// WithComment 添加注释
func WithComment(comment string) Option {
	return func(f *Field) {
		f.Comment = comment
	}
}

// WithIndent 设置缩进级别
func WithIndent(spaces int) Option {
	return func(f *Field) {
		f.Indent = spaces
	}
}

// WithArgs 添加参数
func WithArgs(args ...Argument) Option {
	return func(f *Field) {
		f.Args = append(f.Args, args...)
	}
}

// WithMultilineArgs 使用多行参数格式
func WithMultilineArgs() Option {
	return func(f *Field) {
		f.Multiline = true
	}
}

// New 创建新字段
func New(name string, typeName string, options ...Option) *Field {
	f := getFromPool()
	f.Name = name
	f.Type.Name = typeName

	for _, opt := range options {
		opt(f)
	}

	return f
}
