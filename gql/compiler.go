package gql

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
)

// Compiler GraphQL编译器
type Compiler struct {
	meta *Metadata
}

// NewCompiler 创建一个新的编译器
func NewCompiler(m *Metadata) *Compiler {
	return &Compiler{meta: m}
}

// Compile 编译GraphQL操作为SQL
func (my *Compiler) Compile(operation *ast.OperationDefinition, variables RawMessage) string {
	c := NewContext(my.meta)
	c.Render(operation, variables)
	return c.String()
}

// Dialect 定义SQL方言接口
type Dialect interface {
	// QuoteIdentifier 为标识符添加引号
	QuoteIdentifier(identifier string) string

	// ParamPlaceholder 获取参数占位符 (如: PostgreSQL的$1,$2..., MySQL的?)
	ParamPlaceholder(index int) string

	// FormatLimit 格式化LIMIT子句
	FormatLimit(limit, offset int) string

	// BuildQuery 构建查询语句
	BuildQuery(ctx *Context, set ast.SelectionSet) error

	// BuildMutation 构建变更语句
	BuildMutation(ctx *Context, set ast.SelectionSet) error

	// SupportsReturning 是否支持RETURNING子句
	SupportsReturning() bool

	// SupportsWithCTE 是否支持WITH CTE
	SupportsWithCTE() bool
}

// Context 编译上下文
type Context struct {
	buf        *bytes.Buffer
	meta       *Metadata
	params     []any
	variables  map[string]interface{}
	dictionary map[int]int
}

// NewContext 创建新的编译上下文
func NewContext(m *Metadata) *Context {
	return &Context{
		meta:       m,
		buf:        bytes.NewBuffer([]byte{}),
		dictionary: make(map[int]int),
		variables:  make(map[string]interface{}),
	}
}

// Wrap 包装内容
func (my *Context) Wrap(with string, list ...any) *Context {
	my.Write(with)
	my.Write(list...)
	my.Write(with)
	return my
}

// Write 写入内容
func (my *Context) Write(list ...any) *Context {
	for _, e := range list {
		my.buf.WriteString(fmt.Sprint(e))
	}
	return my
}

// Space 添加空格
func (my *Context) Space(list ...any) *Context {
	my.Wrap(` `, list...)
	return my
}

// Quoted 添加引号
func (my *Context) Quoted(list ...any) *Context {
	my.Wrap(`"`, list...)
	return my
}

// String 获取字符串结果
func (my *Context) String() string {
	return strings.TrimSpace(my.buf.String())
}

// Render 渲染操作
func (my *Context) Render(operation *ast.OperationDefinition, variables RawMessage) {
	_ = json.Unmarshal(variables, &my.variables)
	switch operation.Operation {
	case ast.Query, ast.Subscription:
		my.renderQuery(operation.SelectionSet)
	case ast.Mutation:
		my.renderMutation(operation.SelectionSet)
	}
}

// fieldId 获取字段ID
func (my *Context) fieldId(field *ast.Field) int {
	p := field.GetPosition()
	return p.Line<<32 | p.Column
}

// renderParam 渲染参数
func (my *Context) renderParam(value *ast.Value) {
	val, err := value.Value(my.variables)
	if err != nil {
		my.params = append(my.params, nil)
	} else {
		my.params = append(my.params, val)
	}
	my.Write(`?`)
}

// renderQuery 渲染查询
func (my *Context) renderQuery(set ast.SelectionSet) {
	// TODO: 实现查询渲染
}

// renderMutation 渲染变更
func (my *Context) renderMutation(set ast.SelectionSet) {
	// TODO: 实现变更渲染
}
