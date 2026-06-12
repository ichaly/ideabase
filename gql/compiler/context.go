package compiler

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ichaly/ideabase/gql/protocol"

	"sync"
)

// Context 负责SQL编译过程中的上下文状态，包括SQL拼接、参数、变量、方言等
// 该结构体通过sync.Pool由Compiler统一管理，避免GC压力
// 仅依赖internal，不依赖gql主包

type Context struct {
	buf       *strings.Builder
	quote     string
	slots     []Slot
	counter   int
	hoster    protocol.Hoster
	variables map[string]interface{}
}

// Slot 表示SQL参数槽位：字面量值或变量引用
// 变量引用在执行期解析，使编译产物可按查询文本缓存复用
type Slot struct {
	Value    any    // 字面量值
	Variable string // 变量名，非空时优先生效
}

// Resolve 解析槽位的实际参数值
func (my Slot) Resolve(variables map[string]interface{}) any {
	if my.Variable != "" {
		return variables[my.Variable]
	}
	return my.Value
}

// contextPool 用于Context对象池管理，减少GC压力
var contextPool = sync.Pool{
	New: func() any {
		// 预分配合理容量的Builder和集合，减少动态扩容
		sb := &strings.Builder{}
		sb.Grow(1024) // 预分配1KB初始容量
		return &Context{
			variables: make(map[string]interface{}),
			slots:     make([]Slot, 0, 8),
			buf:       sb,
		}
	},
}

// NewContext 从对象池获取Context实例
func NewContext(h protocol.Hoster, q string, v map[string]interface{}) *Context {
	ctx := contextPool.Get().(*Context)
	ctx.quote = q
	ctx.hoster = h
	ctx.variables = v
	return ctx
}

// Release 归还Context实例到对象池
func (my *Context) Release() {
	my.buf.Reset()
	my.quote = ""
	my.counter = 0
	my.hoster = nil
	my.variables = nil
	my.slots = my.slots[:0]
	contextPool.Put(my)
}

// NextIndex 返回全局自增索引，用于生成不冲突的子查询别名
func (my *Context) NextIndex() int {
	index := my.counter
	my.counter++
	return index
}

// GetClass 按类名（或表名索引）获取类定义
func (my *Context) GetClass(className string) (*protocol.Class, bool) {
	if my.hoster == nil {
		return nil, false
	}
	return my.hoster.GetNode(className)
}

func (my *Context) FindField(className, fieldName string) (*protocol.Field, bool) {
	class, ok := my.GetClass(className)
	if !ok {
		return nil, false
	}
	field, ok := class.Fields[fieldName]
	return field, ok
}

func (my *Context) TableName(className string) (string, bool) {
	if my.hoster == nil {
		return "", false
	}
	class, ok := my.hoster.GetNode(className)
	if !ok || class.Table == "" {
		return "", false
	}
	return class.Table, true
}

// Args 返回参数列表（按当前变量表解析所有槽位）
func (my *Context) Args() []any {
	args := make([]any, len(my.slots))
	for i, slot := range my.slots {
		args[i] = slot.Resolve(my.variables)
	}
	return args
}

// Slots 返回参数槽位列表（拷贝），供编译计划缓存复用
func (my *Context) Slots() []Slot {
	return append([]Slot(nil), my.slots...)
}

// AddParam 添加字面量参数并返回参数序号（从1开始）
func (my *Context) AddParam(value any) int {
	my.slots = append(my.slots, Slot{Value: value})
	return len(my.slots)
}

// AddVariable 添加变量引用参数并返回参数序号（从1开始）
func (my *Context) AddVariable(name string) int {
	my.slots = append(my.slots, Slot{Variable: name})
	return len(my.slots)
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
