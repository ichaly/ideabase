package gql

import (
	"encoding/json"
)

// MarshalJSON 简化版的自定义JSON序列化
func (my *Metadata) MarshalJSON() ([]byte, error) {
	// 使用匿名结构体简化导出
	export := struct {
		Nodes   map[string]interface{}
		Version string
	}{
		Nodes:   make(map[string]interface{}),
		Version: my.Version,
	}

	// 仅导出key和类名相同的节点
	for key, class := range my.Nodes {
		if key == class.Name {
			// 直接使用原始对象，减少字段复制
			export.Nodes[key] = class
		}
	}

	return json.Marshal(export)
}
