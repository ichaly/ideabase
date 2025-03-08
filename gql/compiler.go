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
func (my *Compiler) Compile(operation *ast.OperationDefinition, variables RawMessage) (string, []any) {
	c := newContext(my.meta)
	c.Render(operation, variables)
	return c.String(), c.params
}

// compilerContext 编译上下文
type compilerContext struct {
	buf        *bytes.Buffer
	meta       *Metadata
	params     []any
	variables  map[string]interface{}
	dictionary map[int]int
}

// newContext 创建新的编译上下文
func newContext(m *Metadata) *compilerContext {
	return &compilerContext{
		meta:       m,
		buf:        bytes.NewBuffer([]byte{}),
		dictionary: make(map[int]int),
		variables:  make(map[string]interface{}),
	}
}

// Wrap 包装内容
func (my *compilerContext) Wrap(with string, list ...any) *compilerContext {
	my.Write(with)
	my.Write(list...)
	my.Write(with)
	return my
}

// Write 写入内容
func (my *compilerContext) Write(list ...any) *compilerContext {
	for _, e := range list {
		my.buf.WriteString(fmt.Sprint(e))
	}
	return my
}

// Space 添加空格
func (my *compilerContext) Space(list ...any) *compilerContext {
	my.Wrap(` `, list...)
	return my
}

// Quoted 添加引号
func (my *compilerContext) Quoted(list ...any) *compilerContext {
	my.Wrap(`"`, list...)
	return my
}

// String 获取字符串结果
func (my *compilerContext) String() string {
	return strings.TrimSpace(my.buf.String())
}

// Render 渲染操作
func (my *compilerContext) Render(operation *ast.OperationDefinition, variables RawMessage) {
	_ = json.Unmarshal(variables, &my.variables)
	switch operation.Operation {
	case ast.Query, ast.Subscription:
		my.renderQuery(operation.SelectionSet)
	case ast.Mutation:
		my.renderMutation(operation.SelectionSet)
	}
}

// fieldId 获取字段ID
func (my *compilerContext) fieldId(field *ast.Field) int {
	p := field.GetPosition()
	return p.Line<<32 | p.Column
}

// renderParam 渲染参数
func (my *compilerContext) renderParam(value *ast.Value) {
	val, err := value.Value(my.variables)
	if err != nil {
		my.params = append(my.params, nil)
	} else {
		my.params = append(my.params, val)
	}
	my.Write(`?`)
}

// renderQuery 渲染查询
func (my *compilerContext) renderQuery(selectionSet ast.SelectionSet) {
	// TODO: 实现查询渲染
}

// renderMutation 渲染变更
func (my *compilerContext) renderMutation(selectionSet ast.SelectionSet) {
	// TODO: 实现变更渲染
}
