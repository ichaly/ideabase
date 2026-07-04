package gql

import (
	"strconv"
	"strings"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/std"
)

// Codec 标量编解码器：为一个GraphQL标量类型声明请求边界的双向转换。
// 数据库任何场景（含jsonb）存原始值，转换只发生在出入参；经WithCodecs注册即生效，
// 内置ID codec（NewIdCodec）与业务codec同构，同名后注册者生效（可自定义覆盖）。
// 两方法统一约定：返回nil/原值即不处理。
type Codec interface {
	Name() string // 绑定的标量类型名，如 ID、Phone
	// Encode 出参：原始JSON token字节（数字或含引号字符串）→ 替换token；nil=原样。
	// 返回值须是合法JSON token（字符串token自带引号并保证转义合法）
	Encode(token []byte) []byte
	// Decode 入参：线上值 → 数据库值；原样返回表示不处理
	Decode(value any) any
}

// Matcher Codec的可选扩展：主动认领字段（元数据定型期把命中字段改写为本标量），
// 字段级配置显式指定类型时优先于认领；不实现则仅靠结构推导或配置指到本标量
type Matcher interface {
	Match(class *protocol.Class, field *protocol.Field) bool
}

// Baser Codec的可选扩展：声明底层标量（过滤器操作符集的借用来源），
// 未实现时自定义标量默认借用String的操作符集
type Baser interface {
	Base() string
}

// WithCodecs 注册标量编解码器；认领需改写字段类型，故须在元数据构建期传入
func WithCodecs(codecs ...Codec) MetadataOption {
	return func(opts *metadataOptions) {
		opts.codecs = append(opts.codecs, codecs...)
	}
}

// idCodec 内置ID编解码器：出参编码为sqids shortId，入参还原为数字，
// 与REST通道（fiberJSON）共用std.Id同一套配置
type idCodec struct{}

// NewIdCodec 内置ID codec：需要ID加解密时经WithCodecs注册，不注册则ID保持数字；
// 注册同名codec即可替换为自定义实现
func NewIdCodec() Codec { return idCodec{} }

func (idCodec) Name() string { return protocol.SCALAR_ID }

// Encode 数字token编码为带引号的shortId（~前缀+sqids字母数字，均无需JSON转义）
func (idCodec) Encode(token []byte) []byte {
	id, err := strconv.ParseUint(string(token), 10, 64)
	if err != nil || id == 0 {
		return nil
	}
	short := std.Id(id).Encode()
	out := make([]byte, 0, len(short)+2)
	return append(append(append(out, '"'), short...), '"')
}

// Decode shortId或十进制字符串还原为数字（int64：驱动对数组元素按有符号整型编码，
// 雪花ID恒小于2^63），数字入参原样兼容
func (idCodec) Decode(value any) any {
	if s, ok := value.(string); ok {
		var id std.Id
		if err := id.Decode(s); err == nil && id != 0 {
			return int64(id)
		}
	}
	return value
}

// Match 认领审计列等无外键约束的ID语义列（主外键已由结构推导映射ID，无需认领）；
// 以整型原生类型收窄，避免误伤同名后缀的非ID列
func (idCodec) Match(_ *protocol.Class, field *protocol.Field) bool {
	return field.Type == protocol.SCALAR_INT && strings.HasSuffix(field.Column, "_by")
}
