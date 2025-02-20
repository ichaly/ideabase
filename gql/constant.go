package gql

import (
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/samber/lo"
)

const (
	NONE         internal.ChainKind = ""
	RECURSIVE    internal.ChainKind = "Recursive"
	ONE_TO_MANY  internal.ChainKind = "OneToMany"
	MANY_TO_ONE  internal.ChainKind = "ManyToOne"
	MANY_TO_MANY internal.ChainKind = "ManyToMany"
)

// 参数名称
const (
	DISTINCT   = "distinct"
	LIMIT      = "limit"
	OFFSET     = "offset"
	FIRST      = "first"
	LAST       = "last"
	AFTER      = "after"
	BEFORE     = "before"
	SORT       = "sort"
	WHERE      = "where"
	LEVEL      = "level"
	INSERT     = "insert"
	UPSERT     = "upsert"
	UPDATE     = "update"
	REMOVE     = "delete"
	CONNECT    = "connect"
	DISCONNECT = "disconnect "
)

const (
	PARENTS  = "parents"
	CHILDREN = "children"
)

// GraphQL入参名称后缀
const (
	SUFFIX_SORT_INPUT   = "SortInput"
	SUFFIX_WHERE_INPUT  = "WhereInput"
	SUFFIX_UPSERT_INPUT = "UpsertInput"
	SUFFIX_INSERT_INPUT = "InsertInput"
	SUFFIX_UPDATE_INPUT = "UpdateInput"
)

// 路基表达式后缀
const (
	SUFFIX_EXPRESSION      = "Expression"
	SUFFIX_EXPRESSION_LIST = "ListExpression"
)

// 内置枚举类型
const (
	ENUM_IS_INPUT   = "IsInput"
	ENUM_SORT_INPUT = "SortInput"
)

// 内置枚举类型
const (
	SCALAR_ID        = "ID"
	SCALAR_INT       = "Int"
	SCALAR_DATE      = "Date"
	SCALAR_JSON      = "Json"
	SCALAR_FLOAT     = "Float"
	SCALAR_STRING    = "String"
	SCALAR_CURSOR    = "Cursor"
	SCALAR_BOOLEAN   = "Boolean"
	SCALAR_DATE_TIME = "DateTime"
)

// 过滤操作符号描述
const (
	descIn                 = "Is in list of values"
	descIs                 = "Is value null (true) or not null (false)"
	descEqual              = "Equals value"
	descNotEqual           = "Does not equal value"
	descGreaterThan        = "Is greater than value"
	descGreaterThanOrEqual = "Is greater than or equal to value"
	descLessThan           = "Is less than value"
	descLessThanOrEqual    = "Is less than or equal to value"
	descLike               = "Value matching pattern where '%' represents zero or more characters and '_' represents a single character. Eg. '_r%' finds values having 'r' in second position"
	descILike              = "Value matching (case-insensitive) pattern where '%' represents zero or more characters and '_' represents a single character. Eg. '_r%' finds values not having 'r' in second position"
	descRegex              = "Value matching regular pattern"
	descIRegex             = "Value matching (case-insensitive) regex pattern"
	descLevel              = "Recursive query depth default level 1 , 0 is all."
)

// 逻辑关系操作符常量
const (
	NOT = "not"
	AND = "and"
	OR  = "or"
)

const (
	IS      = "is"
	EQ      = "eq"
	IN      = "in"
	GT      = "gt"
	GE      = "ge"
	LT      = "lt"
	LE      = "le"
	NE      = "ne"
	LIKE    = "like"
	I_LIKE  = "iLike"
	REGEX   = "regex"
	I_REGEX = "iRegex"
)

// 内置的数据库到GraphQL的类型映射
var dataTypes = map[string]string{
	"timestamp with time zone": SCALAR_DATE_TIME,
	"character varying":        SCALAR_STRING,
	"text":                     SCALAR_STRING,
	"smallint":                 SCALAR_INT,
	"integer":                  SCALAR_INT,
	"bigint":                   SCALAR_INT,
	"smallserial":              SCALAR_INT,
	"serial":                   SCALAR_INT,
	"bigserial":                SCALAR_INT,
	"decimal":                  SCALAR_FLOAT,
	"numeric":                  SCALAR_FLOAT,
	"real":                     SCALAR_FLOAT,
	"double precision":         SCALAR_FLOAT,
	"money":                    SCALAR_FLOAT,
	"boolean":                  SCALAR_BOOLEAN,
}

// 顺序不要调整这个会影响内置标量的可用操作符
var operators = []*internal.Symbol{
	{IS, "is", descIs},
	{EQ, "=", descEqual},
	{IN, "in", descIn},
	{GT, ">", descGreaterThan},
	{GE, ">=", descGreaterThanOrEqual},
	{LT, "<", descLessThan},
	{LE, "<=", descLessThanOrEqual},
	{NE, "!=", descNotEqual},
	{LIKE, "like", descLike},
	{I_LIKE, "ilike", descILike},
	{REGEX, "~", descRegex},
	{I_REGEX, "~*", descIRegex},
}

// 构建操作符和内置标量的关系
var symbols = map[string][]*internal.Symbol{
	SCALAR_ID:      operators[1:7], //[eq,in,gt,ge,lt,le]
	SCALAR_INT:     operators[:8],  //[is,eq,in,gt,ge,lt,le,ne]
	SCALAR_FLOAT:   operators[:8],  //[is,eq,in,gt,ge,lt,le,ne]
	SCALAR_STRING:  operators,      //[is,eq,in,gt,ge,lt,le,ne,like,iLike,regex,iRegex]
	SCALAR_BOOLEAN: operators[1:3], //[is,eq]
}

// 运算符按照名字索引字典
var dictionary = lo.Reduce(operators, func(agg map[string]*internal.Symbol, item *internal.Symbol, index int) map[string]*internal.Symbol {
	agg[item.Name] = item
	return agg
}, map[string]*internal.Symbol{})

// 内置标量类型集合
var scalars = []string{SCALAR_ID, SCALAR_INT, SCALAR_FLOAT, SCALAR_STRING, SCALAR_BOOLEAN}
