package utl

import (
	"io"
	"strings"

	jsoniter "github.com/json-iterator/go"
)

// NewJSON 返回一个继承项目扩展能力的 jsoniter API 实例。
// 说明：不要直接复用全局实例去做“场景化”扩展（例如仅 HTTP 输出 shortId），而应基于此创建独立实例。
func NewJSON() jsoniter.API {
	api := jsoniter.ConfigCompatibleWithStandardLibrary
	api.RegisterExtension(&ioTagExtension{})
	return api
}

// 使用项目标准的json序列化
var json = NewJSON()

type ioTagExtension struct{ jsoniter.DummyExtension }

func (my ioTagExtension) UpdateStructDescriptor(sd *jsoniter.StructDescriptor) {
	for _, b := range sd.Fields {
		if j := b.Field.Tag().Get("json"); j != "" {
			name, opts, ok := strings.Cut(j, ",")
			if name != "" && name != "-" {
				if ls := strings.ToLower(name); ls != name {
					b.FromNames = append([]string{ls}, b.FromNames...)
				}
				if us := strings.ToUpper(name); us != name && us != strings.ToLower(name) {
					b.FromNames = append([]string{us}, b.FromNames...)
				}
			}
			if ok {
				for _, tok := range strings.Split(opts, ",") {
					tok = strings.TrimSpace(tok)
					if strings.HasPrefix(tok, "from=") {
						for _, a := range strings.Split(strings.TrimSpace(strings.TrimPrefix(tok, "from=")), "|") {
							if s := strings.TrimSpace(a); s != "" && s != "-" {
								b.FromNames = append([]string{s}, b.FromNames...)
							}
						}
					}
				}
			}
		}
	}
}

func NewDecoder(reader io.Reader) *jsoniter.Decoder {
	return json.NewDecoder(reader)
}

// Unmarshal 解析JSON数据到结构体
func Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// Marshal 将结构体序列化为JSON
func Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// MarshalIndent 将结构体序列化为格式化的JSON
func MarshalIndent(v any, prefix, indent string) ([]byte, error) {
	return json.MarshalIndent(v, prefix, indent)
}
