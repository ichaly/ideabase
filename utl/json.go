package utl

import (
	jsoniter "github.com/json-iterator/go"
)

// 使用项目标准的json序列化
var json = jsoniter.ConfigCompatibleWithStandardLibrary

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
