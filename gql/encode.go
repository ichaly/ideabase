package gql

import (
	"strconv"

	"github.com/vektah/gqlparser/v2/ast"
)

// 标量codec的出入参管线：数据库任何场景（含jsonb）存原始值，转换只发生在请求边界。
// 出参在fetch出口对数据库直通字节做单遍流式改写——由编译期从选择集类型算出的
// codec路径树驱动，仅在路径命中处调用codec转换token，非正则匹配，其余字节零触碰；
// 入参按GraphQL类型声明（变量定义/字面量/递归input对象）调用codec还原。

// ---------- 路径收集（编译期，随执行计划缓存） ----------

// codecPaths 选择集中codec字段的路径树：叶子为Codec实例，否则为子树
type codecPaths map[string]any

// collectCodecPaths 按字段定义类型收集codec路径（列表元素与对象共享同一子树）
func collectCodecPaths(set ast.SelectionSet, meta *Metadata) codecPaths {
	if len(meta.codecs) == 0 {
		return nil
	}
	var out codecPaths
	for _, s := range set {
		f, ok := s.(*ast.Field)
		if !ok || f.Definition == nil {
			continue
		}
		var node any
		if len(f.SelectionSet) > 0 {
			if sub := collectCodecPaths(f.SelectionSet, meta); sub != nil {
				node = sub
			}
		} else if codec := meta.findCodec(f.Definition.Type.Name()); codec != nil {
			node = codec
		}
		if node != nil {
			if out == nil {
				out = codecPaths{}
			}
			out[f.Alias] = node
		}
	}
	return out
}

// ---------- 出参：直通字节的流式转换 ----------

// encodeBytes 对数据库返回的JSON字节做单遍结构化扫描，在codec路径叶子上
// 调用codec改写token；扫描只推进游标不拷贝，首次命中才物化输出缓冲——
// 无命中零分配直接返回原字节。输入结构异常时原样返回（宁可不转换也不破坏响应）。
func encodeBytes(data []byte, paths codecPaths) []byte {
	if len(paths) == 0 || len(data) == 0 {
		return data
	}
	s := codecScanner{src: data}
	s.value(paths)
	s.space()
	if s.bad || s.pos != len(s.src) || s.out == nil {
		return data
	}
	return append(s.out, s.src[s.mark:]...) // 补上末段透传字节
}

// codecScanner 极简JSON流扫描器：只区分对象/数组/字符串/数字/字面量五种形态，
// 携带当前codec路径树节点下行。透传段不逐字节拷贝——mark记录待拷起点，
// 命中改写时才把[mark,token起点)整段拷入out并写入替换token
type codecScanner struct {
	src  []byte
	out  []byte // 首次命中前为nil
	pos  int
	mark int // 待拷贝透传段起点
	bad  bool
}

// patch 把[mark,start)透传段与替换token写入输出，mark推进到token之后
func (my *codecScanner) patch(start int, repl []byte) {
	if my.out == nil {
		my.out = make([]byte, 0, len(my.src)+len(my.src)/8)
	}
	my.out = append(append(my.out, my.src[my.mark:start]...), repl...)
	my.mark = my.pos
}

func (my *codecScanner) space() {
	for my.pos < len(my.src) {
		switch my.src[my.pos] {
		case ' ', '\t', '\n', '\r':
			my.pos++
		default:
			return
		}
	}
}

func (my *codecScanner) value(node any) {
	my.space()
	if my.bad || my.pos >= len(my.src) {
		my.bad = true
		return
	}
	switch c := my.src[my.pos]; {
	case c == '{':
		tree, _ := node.(codecPaths)
		my.object(tree)
	case c == '[':
		my.list(']', func() { my.value(node) }) // 元素共享同一路径节点
	case c == '"':
		my.text(node)
	case c == '-' || (c >= '0' && c <= '9'):
		my.number(node)
	case c == 't' || c == 'f' || c == 'n':
		my.literal()
	default:
		my.bad = true
	}
}

// object 逐键下行：键在路径树中则以对应子节点处理值，否则值整体透传
func (my *codecScanner) object(tree codecPaths) {
	my.list('}', func() {
		key := my.str() // SQL别名源自GraphQL字段名，无转义形态
		my.space()
		if !my.peek(':') {
			my.bad = true
			return
		}
		my.pos++ // ':'
		var child any
		if tree != nil {
			child = tree[string(key)]
		}
		my.value(child)
	})
}

// list 通用"开括号-元素-分隔符-闭括号"骨架，object/array共用
func (my *codecScanner) list(end byte, item func()) {
	my.pos++ // '{' 或 '['
	my.space()
	if my.peek(end) {
		my.pos++
		return
	}
	for !my.bad {
		my.space()
		item()
		my.space()
		switch {
		case my.peek(','):
			my.pos++
		case my.peek(end):
			my.pos++
			return
		default:
			my.bad = true
			return
		}
	}
}

// str 跳过字符串并返回引号内的原始字节（供对象键匹配），纯游标推进零拷贝
func (my *codecScanner) str() []byte {
	if !my.peek('"') {
		my.bad = true
		return nil
	}
	my.pos++
	start := my.pos
	for my.pos < len(my.src) {
		switch my.src[my.pos] {
		case '\\':
			my.pos += 2
		case '"':
			raw := my.src[start:my.pos]
			my.pos++
			return raw
		default:
			my.pos++
		}
	}
	my.bad = true
	return nil
}

// text 字符串值：codec叶子把整个token（含引号）交由codec转换，否则跳过
func (my *codecScanner) text(node any) {
	start := my.pos
	my.str()
	if !my.bad {
		my.encode(start, node)
	}
}

// number 数字值：codec叶子把token交由codec转换（codec自校验形态），否则跳过
func (my *codecScanner) number(node any) {
	start := my.pos
	for my.pos < len(my.src) && numByte(my.src[my.pos]) {
		my.pos++
	}
	my.encode(start, node)
}

// encode 叶子token[start,pos)交由codec转换，命中则打补丁
func (my *codecScanner) encode(start int, node any) {
	if codec, ok := node.(Codec); ok {
		if repl := codec.Encode(my.src[start:my.pos]); repl != nil {
			my.patch(start, repl)
		}
	}
}

func numByte(c byte) bool {
	return c >= '0' && c <= '9' || c == '-' || c == '+' || c == '.' || c == 'e' || c == 'E'
}

// literal 跳过true/false/null
func (my *codecScanner) literal() {
	for my.pos < len(my.src) && my.src[my.pos] >= 'a' && my.src[my.pos] <= 'z' {
		my.pos++
	}
}

func (my *codecScanner) peek(c byte) bool {
	return my.pos < len(my.src) && my.src[my.pos] == c
}

// ---------- 入参：按GraphQL类型声明调用codec还原 ----------

// decodeVariables 依据变量声明类型就地还原变量表（递归进入input对象与列表）
func decodeVariables(schema *ast.Schema, defs ast.VariableDefinitionList, variables map[string]any, meta *Metadata) {
	if len(meta.codecs) == 0 || schema == nil || len(defs) == 0 || len(variables) == 0 {
		return
	}
	for _, def := range defs {
		if v, ok := variables[def.Variable]; ok {
			variables[def.Variable] = decodeTyped(schema, def.Type, v, meta)
		}
	}
}

// decodeTyped 按GraphQL类型走位解码：codec标量叶子解码，input对象逐字段递进
func decodeTyped(schema *ast.Schema, t *ast.Type, v any, meta *Metadata) any {
	if v == nil || t == nil {
		return v
	}
	if t.Elem != nil {
		if list, ok := v.([]any); ok {
			for i, e := range list {
				list[i] = decodeTyped(schema, t.Elem, e, meta)
			}
			return list
		}
		return decodeTyped(schema, t.Elem, v, meta) // 单值按规范强转列表元素
	}
	if codec := meta.findCodec(t.NamedType); codec != nil {
		return codec.Decode(v)
	}
	def := schema.Types[t.NamedType]
	if def == nil || def.Kind != ast.InputObject {
		return v
	}
	if m, ok := v.(map[string]any); ok {
		for k, e := range m {
			if fd := def.Fields.ForName(k); fd != nil {
				m[k] = decodeTyped(schema, fd.Type, e, meta)
			}
		}
	}
	return v
}

// decodeLiterals 就地改写AST中codec标量的字面量（解析期一次，随计划缓存复用）
func decodeLiterals(schema *ast.Schema, set ast.SelectionSet, meta *Metadata) {
	if len(meta.codecs) == 0 {
		return
	}
	for _, s := range set {
		f, ok := s.(*ast.Field)
		if !ok || f.Definition == nil {
			continue
		}
		for _, arg := range f.Arguments {
			if def := f.Definition.Arguments.ForName(arg.Name); def != nil {
				decodeLiteral(schema, def.Type, arg.Value, meta)
			}
		}
		decodeLiterals(schema, f.SelectionSet, meta)
	}
}

// decodeLiteral 按声明类型递归改写一个字面量值（变量引用跳过，由变量解码承担）
func decodeLiteral(schema *ast.Schema, t *ast.Type, val *ast.Value, meta *Metadata) {
	if val == nil || t == nil {
		return
	}
	switch val.Kind {
	case ast.ListValue:
		elem := t.Elem
		if elem == nil {
			elem = t
		}
		for _, c := range val.Children {
			decodeLiteral(schema, elem, c.Value, meta)
		}
	case ast.ObjectValue:
		def := schema.Types[t.Name()]
		if def == nil {
			return
		}
		for _, c := range val.Children {
			if fd := def.Fields.ForName(c.Name); fd != nil {
				decodeLiteral(schema, fd.Type, c.Value, meta)
			}
		}
	case ast.StringValue:
		codec := meta.findCodec(t.Name())
		if codec == nil {
			return
		}
		switch decoded := codec.Decode(val.Raw).(type) {
		case int64:
			val.Raw, val.Kind = strconv.FormatInt(decoded, 10), ast.IntValue
		case string:
			val.Raw = decoded
		}
	}
}
