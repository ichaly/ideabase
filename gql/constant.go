package gql

import (
	"github.com/ichaly/ideabase/gql/protocol"

	jsoniter "github.com/json-iterator/go"
)

// 全局JSON处理实例，使用jsoniter替代标准库
var json = jsoniter.ConfigCompatibleWithStandardLibrary

// Operator 过滤操作符（定义在protocol包，对外保留别名）
type Operator = protocol.Operator
