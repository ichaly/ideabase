package gql

import (
	"github.com/ichaly/ideabase/gql/protocol"

	jsoniter "github.com/json-iterator/go"
)

// 全局JSON处理实例，使用jsoniter替代标准库
var json = jsoniter.ConfigCompatibleWithStandardLibrary

// 数据库结果解码专用：UseNumber保留json.Number，避免bigint主键(如雪花ID>2^53)经float64丢精度
var jsonNumeric = jsoniter.Config{EscapeHTML: true, SortMapKeys: true, ValidateJsonRawMessage: true, UseNumber: true}.Froze()

// 响应序列化专用：不校验RawMessage（懒解包直通段来自数据库构造即合法，
// 校验会整段重解析，实测直通序列化慢约20倍）
var jsonReply = jsoniter.Config{EscapeHTML: true, SortMapKeys: true}.Froze()

// Operator 过滤操作符（定义在protocol包，对外保留别名）
type Operator = protocol.Operator
