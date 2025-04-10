package gql

import (
	"fmt"
	"strings"
	"sync"

	"github.com/vektah/gqlparser/v2/ast"
)

// Dialect 定义SQL方言接口
type Dialect interface {
	// Placeholder 获取参数占位符 (如: PostgreSQL的$1,$2..., MySQL的?)
	Placeholder(index int) string

	// FormatLimit 格式化LIMIT子句
	FormatLimit(limit, offset int) string

	// BuildQuery 构建查询语句
	BuildQuery(cpl *Compiler, set ast.SelectionSet) error

	// BuildMutation 构建变更语句
	BuildMutation(cpl *Compiler, set ast.SelectionSet) error
}

// Compiler 编译上下文
type Compiler struct {
	buf       *strings.Builder
	meta      *Metadata // 元数据引用
	dialect   Dialect   // 方言实现引用，避免重复查询
	params    []any
	variables map[string]interface{}
}

// 上下文对象池，用于减少GC压力
var compilerPool = sync.Pool{
	New: func() interface{} {
		// 预分配合理容量的Builder和集合，减少动态扩容
		sb := &strings.Builder{}
		sb.Grow(1024) // 预分配1KB初始容量
		return &Compiler{
			buf:       sb,
			params:    make([]any, 0, 8),
			variables: make(map[string]interface{}, 8),
		}
	},
}

// NewCompiler 创建新的编译上下文
func NewCompiler(m *Metadata, d Dialect) *Compiler {
	cpl := compilerPool.Get().(*Compiler)
	// 重置缓冲区而不是创建新的
	cpl.buf.Reset()
	// 清空但重用现有map和slice以避免内存分配
	for k := range cpl.variables {
		delete(cpl.variables, k)
	}
	cpl.meta = m
	cpl.dialect = d
	cpl.params = cpl.params[:0]
	// 预留初始容量以减少重新分配
	cpl.buf.Grow(1024)
	return cpl
}

// Release 释放上下文回到对象池
func (my *Compiler) Release() {
	// 将对象放回池中以便重用
	compilerPool.Put(my)
}

// Wrap 包装内容
func (my *Compiler) Wrap(with string, list ...any) *Compiler {
	my.Write(with)
	my.Write(list...)
	my.Write(with)
	return my
}

// Write 写入内容
func (my *Compiler) Write(list ...any) *Compiler {
	for _, e := range list {
		my.buf.WriteString(fmt.Sprint(e))
	}
	return my
}

// Space 添加空格
func (my *Compiler) Space(list ...any) *Compiler {
	my.Wrap(` `, list...)
	return my
}

// Quoted 添加引号
func (my *Compiler) Quoted(list ...any) *Compiler {
	my.Wrap(`"`, list...)
	return my
}

// String 获取字符串结果
func (my *Compiler) String() string {
	return strings.TrimSpace(my.buf.String())
}

// Args 获取参数列表
func (my *Compiler) Args() []interface{} {
	return my.params
}

// Build 渲染操作
func (my *Compiler) Build(operation *ast.OperationDefinition, variables map[string]interface{}) {
	my.variables = variables
	switch operation.Operation {
	case ast.Query, ast.Subscription:
		my.renderQuery(operation.SelectionSet)
	case ast.Mutation:
		my.renderMutation(operation.SelectionSet)
	}
}

// renderQuery 渲染查询
func (my *Compiler) renderQuery(set ast.SelectionSet) {
	// TODO: 实现查询渲染
}

// renderMutation 渲染变更
func (my *Compiler) renderMutation(set ast.SelectionSet) {
	// TODO: 实现变更渲染
}
