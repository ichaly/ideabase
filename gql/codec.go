package gql

import (
	"strconv"
	"strings"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/std"
)

// Codec 标量编解码器：为一个GraphQL标量类型声明请求边界的双向转换。
// 数据库任何场景（含jsonb）存原始值，转换只发生在出入参；注册即生效，
// 内置ID codec（metadata.encode-id开启时注册）与业务codec同构，同名可覆盖。
type Codec interface {
	Name() string // 绑定的标量类型名，如 ID、Phone
	Base() string // 底层标量（过滤器操作符集的借用来源）；内置标量返回自身
	// Encode 出参：原始JSON token字节（数字或带引号字符串）→ 替换token；false=原样
	Encode(token []byte) ([]byte, bool)
	// Decode 入参：线上值 → 数据库值；原样返回表示不处理
	Decode(value any) any
}

// Matcher Codec的可选扩展：主动认领字段（元数据定型期把命中字段改写为本标量），
// 字段级配置显式指定类型时优先于认领；不实现则仅靠结构推导或配置指到本标量
type Matcher interface {
	Match(class *protocol.Class, field *protocol.Field) bool
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

// NewIdCodec 创建内置ID codec（metadata.encode-id开启时自动注册）
func NewIdCodec() Codec { return idCodec{} }

func (idCodec) Name() string { return protocol.SCALAR_ID }
func (idCodec) Base() string { return protocol.SCALAR_ID }

func (idCodec) Encode(token []byte) ([]byte, bool) {
	id, err := strconv.ParseUint(string(token), 10, 64)
	if err != nil || id == 0 {
		return nil, false
	}
	return strconv.AppendQuote(nil, std.Id(id).Encode()), true
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

// Match 认领审计列等无外键约束的ID语义列（主外键已由结构推导映射ID，无需认领）
func (idCodec) Match(_ *protocol.Class, field *protocol.Field) bool {
	return strings.HasSuffix(field.Column, "_by")
}
