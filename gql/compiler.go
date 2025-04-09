package gql

import (
	"fmt"
	"strings"
	"sync"

	"github.com/vektah/gqlparser/v2/ast"
)

// 方言注册表
var dialects = make(map[string]Dialect)

// RegisterDialect 全局注册方法，用于各个方言包在init函数中调用
func RegisterDialect(name string, dialect Dialect) {
	dialects[name] = dialect
}

// Compiler GraphQL编译器
type Compiler struct {
	meta    *Metadata
	dialect Dialect
}

// NewCompiler 创建一个新的编译器
func NewCompiler(m *Metadata) (*Compiler, error) {
	compiler := &Compiler{
		meta: m,
	}

	// 方言选择逻辑
	// 1. 首先尝试根据数据库类型选择方言
	if m != nil && m.db != nil {
		dbName := m.db.Name()
		
		// 根据数据库驱动名称匹配方言
		switch {
		case strings.Contains(dbName, "postgres"):
			if dialect, ok := dialects["postgresql"]; ok {
				compiler.dialect = dialect
			}
		case strings.Contains(dbName, "mysql"):
			if dialect, ok := dialects["mysql"]; ok {
				compiler.dialect = dialect
			}
		}
	}
	
	// 2. 如果未找到匹配方言，尝试使用PostgreSQL方言（如果存在）
	if compiler.dialect == nil && len(dialects) > 0 {
		if dialect, ok := dialects["postgresql"]; ok {
			compiler.dialect = dialect
		} else {
			// 3. 否则使用第一个可用的方言
			for _, dialect := range dialects {
				compiler.dialect = dialect
				break
			}
		}
	}
	
	// 4. 如果仍未找到方言，返回错误
	if compiler.dialect == nil {
		return nil, fmt.Errorf("没有可用的SQL方言实现，请确保导入了相应的dialect包")
	}

	return compiler, nil
}

// Compile 编译GraphQL操作为SQL
func (my *Compiler) Compile(operation *ast.OperationDefinition, variables RawMessage) (string, []interface{}) {
	c := NewContext(my.meta)
	defer c.Release()      // 使用完毕后释放回对象池
	c.dialect = my.dialect // 设置共享的方言实现
	c.Build(operation, variables)
	return c.String(), c.Args()
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
	buf        *strings.Builder
	meta       *Metadata // 元数据引用
	dialect    Dialect   // 方言实现引用，避免重复查询
	params     []any
	variables  map[string]interface{}
	dictionary map[int]int
}

// 上下文对象池，用于减少GC压力
var contextPool = sync.Pool{
	New: func() interface{} {
		// 预分配合理容量的Builder和集合，减少动态扩容
		sb := &strings.Builder{}
		sb.Grow(1024) // 预分配1KB初始容量
		return &Context{
			buf:        sb,
			dictionary: make(map[int]int, 8),
			variables:  make(map[string]interface{}, 8),
			params:     make([]any, 0, 8),
		}
	},
}

// NewContext 创建新的编译上下文
func NewContext(m *Metadata) *Context {
	ctx := contextPool.Get().(*Context)
	// 重置缓冲区而不是创建新的
	ctx.buf.Reset()
	// 清空但重用现有map和slice以避免内存分配
	for k := range ctx.dictionary {
		delete(ctx.dictionary, k)
	}
	for k := range ctx.variables {
		delete(ctx.variables, k)
	}
	ctx.params = ctx.params[:0]
	ctx.meta = m
	// 预留初始容量以减少重新分配
	ctx.buf.Grow(1024)
	return ctx
}

// Release 释放上下文回到对象池
func (my *Context) Release() {
	// 将对象放回池中以便重用
	contextPool.Put(my)
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

// Args 获取参数列表
func (my *Context) Args() []interface{} {
	return my.params
}

// Build 渲染操作
func (my *Context) Build(operation *ast.OperationDefinition, variables RawMessage) {
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
