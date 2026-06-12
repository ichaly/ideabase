package gql

import (
	"github.com/ichaly/ideabase/gql/protocol"

	jsoniter "github.com/json-iterator/go"
)

// 全局JSON处理实例，使用jsoniter替代标准库
var json = jsoniter.ConfigCompatibleWithStandardLibrary

// Operator 过滤操作符（定义在protocol包，此处保留别名）
type Operator = protocol.Operator

// 协议常量统一定义在protocol包（方言实现也依赖），此处保留同名别名
const (
	TYPE_SORT_DIRECTION  = protocol.TYPE_SORT_DIRECTION
	TYPE_PAGE_INFO       = protocol.TYPE_PAGE_INFO
	TYPE_GROUP_BY        = protocol.TYPE_GROUP_BY
	TYPE_NUMBER_STATS    = protocol.TYPE_NUMBER_STATS
	TYPE_STRING_STATS    = protocol.TYPE_STRING_STATS
	TYPE_DATE_TIME_STATS = protocol.TYPE_DATE_TIME_STATS

	SUFFIX_STATS        = protocol.SUFFIX_STATS
	SUFFIX_GROUP        = protocol.SUFFIX_GROUP
	SUFFIX_RESULT       = protocol.SUFFIX_RESULT
	SUFFIX_SORT_INPUT   = protocol.SUFFIX_SORT_INPUT
	SUFFIX_WHERE_INPUT  = protocol.SUFFIX_WHERE_INPUT
	SUFFIX_CREATE_INPUT = protocol.SUFFIX_CREATE_INPUT
	SUFFIX_UPDATE_INPUT = protocol.SUFFIX_UPDATE_INPUT
	SUFFIX_UPSERT_INPUT = protocol.SUFFIX_UPSERT_INPUT
	SUFFIX_INSERT_INPUT = protocol.SUFFIX_INSERT_INPUT

	ID         = protocol.ID
	INPUT      = protocol.INPUT
	DISTINCT   = protocol.DISTINCT
	LIMIT      = protocol.LIMIT
	OFFSET     = protocol.OFFSET
	FIRST      = protocol.FIRST
	LAST       = protocol.LAST
	AFTER      = protocol.AFTER
	BEFORE     = protocol.BEFORE
	SORT       = protocol.SORT
	WHERE      = protocol.WHERE
	LEVEL      = protocol.LEVEL
	INSERT     = protocol.INSERT
	CREATE     = protocol.CREATE
	UPSERT     = protocol.UPSERT
	UPDATE     = protocol.UPDATE
	DELETE     = protocol.DELETE
	CONNECT    = protocol.CONNECT
	DISCONNECT = protocol.DISCONNECT
	GROUP_BY   = protocol.GROUP_BY
	SEARCH     = protocol.SEARCH

	TOTAL     = protocol.TOTAL
	ITEMS     = protocol.ITEMS
	PAGE_INFO = protocol.PAGE_INFO
	PARENTS   = protocol.PARENTS
	CHILDREN  = protocol.CHILDREN

	FUNCTION_SUM            = protocol.FUNCTION_SUM
	FUNCTION_AVG            = protocol.FUNCTION_AVG
	FUNCTION_MIN            = protocol.FUNCTION_MIN
	FUNCTION_MAX            = protocol.FUNCTION_MAX
	FUNCTION_KEY            = protocol.FUNCTION_KEY
	FUNCTION_COUNT          = protocol.FUNCTION_COUNT
	FUNCTION_COUNT_DISTINCT = protocol.FUNCTION_COUNT_DISTINCT

	SUFFIX_EXPRESSION      = protocol.SUFFIX_EXPRESSION
	SUFFIX_EXPRESSION_LIST = protocol.SUFFIX_EXPRESSION_LIST

	ENUM_IS_INPUT   = protocol.ENUM_IS_INPUT
	ENUM_SORT_INPUT = protocol.ENUM_SORT_INPUT

	SCALAR_ID        = protocol.SCALAR_ID
	SCALAR_INT       = protocol.SCALAR_INT
	SCALAR_DATE      = protocol.SCALAR_DATE
	SCALAR_JSON      = protocol.SCALAR_JSON
	SCALAR_FLOAT     = protocol.SCALAR_FLOAT
	SCALAR_STRING    = protocol.SCALAR_STRING
	SCALAR_CURSOR    = protocol.SCALAR_CURSOR
	SCALAR_BOOLEAN   = protocol.SCALAR_BOOLEAN
	SCALAR_DATE_TIME = protocol.SCALAR_DATE_TIME

	NOT = protocol.NOT
	AND = protocol.AND
	OR  = protocol.OR

	IS          = protocol.IS
	EQ          = protocol.EQ
	IN          = protocol.IN
	NI          = protocol.NI
	GT          = protocol.GT
	GE          = protocol.GE
	LT          = protocol.LT
	LE          = protocol.LE
	NE          = protocol.NE
	LIKE        = protocol.LIKE
	I_LIKE      = protocol.I_LIKE
	REGEX       = protocol.REGEX
	I_REGEX     = protocol.I_REGEX
	HAS_KEY     = protocol.HAS_KEY
	HAS_KEY_ANY = protocol.HAS_KEY_ANY
	HAS_KEY_ALL = protocol.HAS_KEY_ALL
)

// 集合与查询函数别名
var (
	dataTypes = protocol.DataTypes
	grouping  = protocol.Grouping
	scalars   = protocol.Scalars
)

// GetOperator 根据名称获取操作符信息
func GetOperator(name string) (*Operator, bool) {
	return protocol.GetOperator(name)
}
