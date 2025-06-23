package compiler

import (
	"fmt"
	"github.com/ichaly/ideabase/gql/protocol"
	"strconv"
	"strings"

	"sync"
)

// Context 负责SQL编译过程中的上下文状态，包括SQL拼接、参数、变量、方言等
// 该结构体通过sync.Pool由Compiler统一管理，避免GC压力
// 仅依赖internal，不依赖gql主包

type Context struct {
	buf       *strings.Builder
	quote     string
	params    []any
	variables map[string]interface{}
}

// contextPool 用于Context对象池管理，减少GC压力
var contextPool = sync.Pool{
	New: func() any {
		// 预分配合理容量的Builder和集合，减少动态扩容
		sb := &strings.Builder{}
		sb.Grow(1024) // 预分配1KB初始容量
		return &Context{
			variables: make(map[string]interface{}),
			params:    make([]any, 0, 8),
			buf:       sb,
		}
	},
}

// NewContext 从对象池获取Context实例
func NewContext(q string, v map[string]interface{}) *Context {
	ctx := contextPool.Get().(*Context)
	ctx.quote = q
	ctx.variables = v
	return ctx
}

// Release 归还Context实例到对象池
func (my *Context) Release() {
	my.buf.Reset()
	my.quote = ""
	my.variables = nil
	my.params = my.params[:0]
	contextPool.Put(my)
}

func (my *Context) FindField(className, fieldName string) (*protocol.Field, bool) {
	return nil, false
}

func (my *Context) TableName(param string) (string, bool) {
	return "", false
}

// Args 返回参数列表
func (my *Context) Args() []any {
	return my.params
}

// AddParam 添加参数并返回参数索引
func (my *Context) AddParam(value any) int {
	my.params = append(my.params, value)
	return len(my.params)
}

// String 获取当前SQL字符串
func (my *Context) String() string {
	return strings.TrimSpace(my.buf.String())
}

// Write 写入SQL片段或参数到Buffer
func (my *Context) Write(args ...any) *Context {
	for _, e := range args {
		switch v := e.(type) {
		case string:
			my.buf.WriteString(v)
		case int:
			my.buf.WriteString(strconv.Itoa(v))
		case int64:
			my.buf.WriteString(strconv.FormatInt(v, 10))
		case float64:
			my.buf.WriteString(strconv.FormatFloat(v, 'f', -1, 64))
		case bool:
			if v {
				my.buf.WriteString("true")
			} else {
				my.buf.WriteString("false")
			}
		case []byte:
			my.buf.Write(v)
		case fmt.Stringer:
			my.buf.WriteString(v.String())
		default:
			my.buf.WriteString(fmt.Sprint(v))
		}
	}
	return my
}

// Wrap 包装内容
func (my *Context) Wrap(with string, list ...any) *Context {
	my.Write(with)
	my.Write(list...)
	my.Write(with)
	return my
}

// Space 添加空格并写入内容(可选)
func (my *Context) Space(content ...any) *Context {
	return my.SpaceBefore(content...).SpaceAfter()
}

// SpaceBefore 在前面添加空格，内容可选
func (my *Context) SpaceBefore(content ...any) *Context {
	my.buf.WriteString(" ")
	if len(content) > 0 {
		my.Write(content...)
	}
	return my
}

// SpaceAfter 在后面添加空格，内容可选
func (my *Context) SpaceAfter(content ...any) *Context {
	if len(content) > 0 {
		my.Write(content...)
	}
	my.buf.WriteString(" ")
	return my
}

// Quote 添加引号
func (my *Context) Quote(list ...any) *Context {
	return my.Wrap(my.quote, list...)
}

// QuotedWithSpace 添加引号和空格
func (my *Context) QuotedWithSpace(content any) *Context {
	return my.SpaceBefore().Quote(content).SpaceAfter()
}
