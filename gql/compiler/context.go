package compiler

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/ichaly/ideabase/gql/protocol"
)

// Context 负责SQL编译过程中的上下文状态，包括SQL拼接、参数、变量、方言等
// 该结构体通过sync.Pool由Compiler统一管理，避免GC压力
// 仅依赖internal，不依赖gql主包

type Context struct {
	buf       *strings.Builder
	quote     string
	slots     []Slot
	counter   int
	volatile  bool
	hoster    protocol.Hoster
	variables map[string]interface{}
	tables    map[string]bool
}

// Slot 表示SQL参数槽位：字面量值或变量引用
// 变量引用在执行期解析，使编译产物可按查询文本缓存复用
type Slot struct {
	Value    any    // 字面量值
	Variable string // 变量名，非空时优先生效
	Cursor   int    // >=0时变量为base64游标，解码JSON数组后取第Cursor个键值
}

// Resolve 解析槽位的实际参数值
func (my Slot) Resolve(variables map[string]interface{}) any {
	if my.Variable == "" {
		return my.Value
	}
	value := variables[my.Variable]
	if my.Cursor >= 0 {
		return cursorElement(value, my.Cursor)
	}
	return value
}

// cursorElement 解码游标并取键值：base64(JSON数组)
func cursorElement(value any, index int) any {
	text, ok := value.(string)
	if !ok {
		return nil
	}
	keys, err := DecodeCursor(text)
	if err != nil || index >= len(keys) {
		return nil
	}
	return keys[index]
}

// DecodeCursor 解码游标为排序键值数组
func DecodeCursor(cursor string) ([]any, error) {
	data, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return nil, fmt.Errorf("无效的游标: %w", err)
	}
	var keys []any
	if err = json.Unmarshal(data, &keys); err != nil {
		return nil, fmt.Errorf("无效的游标内容: %w", err)
	}
	return keys, nil
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
	my.volatile = false
	my.hoster = nil
	my.variables = nil
	my.tables = nil
	my.slots = my.slots[:0]
	contextPool.Put(my)
}

// Variable 返回变量值
func (my *Context) Variable(name string) (interface{}, bool) {
	value, ok := my.variables[name]
	return value, ok
}

// MarkTable 记录本次编译涉及的表，订阅按表变更唤醒
func (my *Context) MarkTable(name string) {
	if my.tables == nil {
		my.tables = make(map[string]bool, 4)
	}
	my.tables[name] = true
}

// Tables 返回本次编译涉及的表集合
func (my *Context) Tables() []string {
	tables := make([]string, 0, len(my.tables))
	for name := range my.tables {
		tables = append(tables, name)
	}
	return tables
}

// MarkVolatile 标记编译产物依赖变量内容（如整体input变量），不可按查询文本缓存
func (my *Context) MarkVolatile() {
	my.volatile = true
}

// Volatile 编译产物是否依赖变量内容
func (my *Context) Volatile() bool {
	return my.volatile
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
	my.slots = append(my.slots, Slot{Value: value, Cursor: -1})
	return len(my.slots)
}

// AddVariable 添加变量引用参数并返回参数序号（从1开始）
func (my *Context) AddVariable(name string) int {
	my.slots = append(my.slots, Slot{Variable: name, Cursor: -1})
	return len(my.slots)
}

// AddCursor 添加游标键值参数：执行期解码变量游标取第index个键值
func (my *Context) AddCursor(name string, index int) int {
	my.slots = append(my.slots, Slot{Variable: name, Cursor: index})
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
