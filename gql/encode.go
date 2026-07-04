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
// 调用codec改写token，其余字节原样拷贝；输入结构异常时原样返回（宁可不转换
// 也不破坏响应）。零解包零重序列化，直通快路径得以保留。
func encodeBytes(data []byte, paths codecPaths) []byte {
	if len(paths) == 0 || len(data) == 0 {
		return data
	}
	s := codecScanner{src: data, out: make([]byte, 0, len(data)+len(data)/8)}
	s.value(paths)
	s.space()
	if s.bad || s.pos != len(s.src) {
		return data
	}
	return s.out
}

// codecScanner 极简JSON流扫描器：只区分对象/数组/字符串/数字/字面量五种形态，
// 携带当前codec路径树节点下行；叶子token交由codec转换，其余全部透传
type codecScanner struct {
	src []byte
	out []byte
	pos int
	bad bool
}

func (my *codecScanner) space() {
	for my.pos < len(my.src) {
		switch my.src[my.pos] {
		case ' ', '\t', '\n', '\r':
			my.out = append(my.out, my.src[my.pos])
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
		my.array(node)
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
	my.copyByte() // '{'
	my.space()
	if my.peek('}') {
		my.copyByte()
		return
	}
	for !my.bad {
		my.space()
		key := my.str() // SQL别名源自GraphQL字段名，无转义形态
		my.space()
		if !my.peek(':') {
			my.bad = true
			return
		}
		my.copyByte() // ':'
		var child any
		if tree != nil {
			child = tree[string(key)]
		}
		my.value(child)
		my.space()
		switch {
		case my.peek(','):
			my.copyByte()
		case my.peek('}'):
			my.copyByte()
			return
		default:
			my.bad = true
			return
		}
	}
}

// array 元素共享同一路径节点（对象列表下行子树，标量列表逐元素转换）
func (my *codecScanner) array(node any) {
	my.copyByte() // '['
	my.space()
	if my.peek(']') {
		my.copyByte()
		return
	}
	for !my.bad {
		my.value(node)
		my.space()
		switch {
		case my.peek(','):
			my.copyByte()
		case my.peek(']'):
			my.copyByte()
			return
		default:
			my.bad = true
			return
		}
	}
}

// str 透传字符串并返回引号内的原始字节（供对象键匹配）
func (my *codecScanner) str() []byte {
	if !my.peek('"') {
		my.bad = true
		return nil
	}
	my.copyByte()
	start := my.pos
	for my.pos < len(my.src) {
		switch my.src[my.pos] {
		case '\\':
			if my.pos+1 >= len(my.src) {
				my.bad = true
				return nil
			}
			my.out = append(my.out, my.src[my.pos], my.src[my.pos+1])
			my.pos += 2
		case '"':
			raw := my.src[start:my.pos]
			my.copyByte()
			return raw
		default:
			my.out = append(my.out, my.src[my.pos])
			my.pos++
		}
	}
	my.bad = true
	return nil
}

// text 字符串值：codec叶子把整个token（含引号）交由codec转换，否则透传
func (my *codecScanner) text(node any) {
	codec, ok := node.(Codec)
	if !ok {
		my.str()
		return
	}
	mark := len(my.out)
	my.str()
	if my.bad {
		return
	}
	if repl, hit := codec.Encode(my.out[mark:]); hit {
		my.out = append(my.out[:mark], repl...)
	}
}

// number 数字值：codec叶子把纯数字token交由codec转换，负数/小数/科学计数透传
func (my *codecScanner) number(node any) {
	start, digits := my.pos, true
	for my.pos < len(my.src) {
		c := my.src[my.pos]
		if c >= '0' && c <= '9' {
			my.pos++
			continue
		}
		if c == '-' || c == '+' || c == '.' || c == 'e' || c == 'E' {
			digits = false
			my.pos++
			continue
		}
		break
	}
	run := my.src[start:my.pos]
	if codec, ok := node.(Codec); ok && digits {
		if repl, hit := codec.Encode(run); hit {
			my.out = append(my.out, repl...)
			return
		}
	}
	my.out = append(my.out, run...)
}

// literal 透传true/false/null
func (my *codecScanner) literal() {
	for my.pos < len(my.src) {
		c := my.src[my.pos]
		if c >= 'a' && c <= 'z' {
			my.copyByte()
			continue
		}
		return
	}
}

func (my *codecScanner) peek(c byte) bool {
	return my.pos < len(my.src) && my.src[my.pos] == c
}

func (my *codecScanner) copyByte() {
	my.out = append(my.out, my.src[my.pos])
	my.pos++
}

// ---------- 入参：按GraphQL类型声明调用codec还原 ----------

// decodeVariables 依据变量声明类型就地还原变量表（递归进入input对象与列表）
func decodeVariables(schema *ast.Schema, defs ast.VariableDefinitionList, variables map[string]any, meta *Metadata) {
	if schema == nil || len(defs) == 0 || len(variables) == 0 {
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
