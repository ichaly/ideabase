package utl

import (
	"strings"

	jsoniter "github.com/json-iterator/go"
)

// 使用项目标准的json序列化
var json = jsoniter.ConfigCompatibleWithStandardLibrary

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

func init() {
	json.RegisterExtension(&ioTagExtension{})
}

// UnmarshalJSON 解析JSON数据到结构体
func UnmarshalJSON(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// MarshalJSON 将结构体序列化为JSON
func MarshalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

// MarshalIndentJSON 将结构体序列化为格式化的JSON
func MarshalIndentJSON(v any, prefix, indent string) ([]byte, error) {
	return json.MarshalIndent(v, prefix, indent)
}
