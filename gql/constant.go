package gql

import (
	"github.com/ichaly/ideabase/gql/protocol"

	jsoniter "github.com/json-iterator/go"
)

// 全局JSON处理实例，使用jsoniter替代标准库
var json = jsoniter.ConfigCompatibleWithStandardLibrary

// 数据库结果解码专用：UseNumber保留json.Number，避免bigint主键(如雪花ID>2^53)经float64丢精度
var jsonNumeric = jsoniter.Config{EscapeHTML: true, SortMapKeys: true, ValidateJsonRawMessage: true, UseNumber: true}.Froze()

// Operator 过滤操作符（定义在protocol包，对外保留别名）
type Operator = protocol.Operator
