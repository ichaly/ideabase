package gql

import (
	"fmt"
	"strings"
	"sync"

	"github.com/vektah/gqlparser/v2/ast"
)

// Dialect 定义SQL方言接口
type Dialect interface {
	// QuoteIdentifier 为标识符添加引号
	QuoteIdentifier() string

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

// Args 获取参数列表
func (my *Compiler) Args() []interface{} {
	return my.params
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

// Space 添加空格并写入内容(可选)
func (my *Compiler) Space(content ...any) *Compiler {
	return my.SpaceBefore(content...).SpaceAfter()
}

// SpaceBefore 在前面添加空格，内容可选
func (my *Compiler) SpaceBefore(content ...any) *Compiler {
	my.buf.WriteString(" ")
	if len(content) > 0 {
		my.Write(content...)
	}
	return my
}

// SpaceAfter 在后面添加空格，内容可选
func (my *Compiler) SpaceAfter(content ...any) *Compiler {
	if len(content) > 0 {
		my.Write(content...)
	}
	my.buf.WriteString(" ")
	return my
}

// Quote 添加引号
func (my *Compiler) Quote(list ...any) *Compiler {
	my.Wrap(my.dialect.QuoteIdentifier(), list...)
	return my
}

// QuotedWithSpace 添加引号和空格
func (my *Compiler) QuotedWithSpace(content any) *Compiler {
	return my.SpaceBefore().Quote(content).SpaceAfter()
}

// String 获取字符串结果
func (my *Compiler) String() string {
	return strings.TrimSpace(my.buf.String())
}

func (my *Compiler) Build(operation *ast.OperationDefinition, variables map[string]interface{}) {
	my.variables = variables
	switch operation.Operation {
	case ast.Query, ast.Subscription:
		my.dialect.BuildQuery(my, operation.SelectionSet)
	case ast.Mutation:
		my.dialect.BuildMutation(my, operation.SelectionSet)
	}
}

// AddParam 添加参数并返回参数索引
func (my *Compiler) AddParam(value any) int {
	my.params = append(my.params, value)
	return len(my.params)
}
