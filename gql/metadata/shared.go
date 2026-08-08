package metadata

import (
	"encoding/json"
	"fmt"
)

// Loader名称常量
const (
	LoaderFile   = "file"
	LoaderPgsql  = "pgsql"
	LoaderMysql  = "mysql"
	LoaderConfig = "config"
)

// NullableType 自定义类型，用于处理MySQL和PostgreSQL的可空字段
type NullableType bool

func (my NullableType) Bool() bool {
	return bool(my)
}

func (my *NullableType) UnmarshalJSON(data []byte) error {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	switch value := v.(type) {
	case bool:
		*my = NullableType(value)
	case float64:
		*my = value != 0
	case string:
		*my = value == "1" || value == "true"
	default:
		return fmt.Errorf("unexpected type for nullable: %T", v)
	}
	return nil
}

// tableInfo 表信息结构
// 供所有Loader和dbLoader共用
type tableInfo struct {
	Schema           string `json:"schema"`
	TableName        string `json:"table_name" gorm:"column:table_name"`
	TableDescription string `json:"table_description" gorm:"column:table_description"`
	TableKind        string `json:"table_kind"`
}

type columnInfo struct {
	TableName            string       `json:"table_name" gorm:"column:table_name"`
	ColumnName           string       `json:"column_name" gorm:"column:column_name"`
	DataType             string       `json:"data_type" gorm:"column:data_type"`
	IsNullable           NullableType `json:"is_nullable" gorm:"column:is_nullable"`
	CharMaxLength        *int64       `json:"character_maximum_length" gorm:"column:character_maximum_length"`
	NumericPrecision     *int64       `json:"numeric_precision" gorm:"column:numeric_precision"`
	NumericScale         *int64       `json:"numeric_scale" gorm:"column:numeric_scale"`
	ColumnDescription    string       `json:"column_description" gorm:"column:column_description"`
	Ordinal              int          `json:"ordinal"`
	DefaultExpression    string       `json:"default_expression"`
	GenerationKind       string       `json:"generation_kind"`
	GenerationExpression string       `json:"generation_expression"`
	TypeSchema           string       `json:"type_schema"`
	TypeName             string       `json:"type_name"`
	TypeKind             string       `json:"type_kind"`
	BaseType             string       `json:"base_type"`
	ElementSchema        string       `json:"element_schema"`
	ElementName          string       `json:"element_name"`
	ElementKind          string       `json:"element_kind"`
	ElementBase          string       `json:"element_base"`
	EnumLabels           []string     `json:"enum_labels"`
}

type primaryKeyInfo struct {
	TableName  string `json:"table_name" gorm:"column:table_name"`
	ColumnName string `json:"column_name" gorm:"column:column_name"`
}

type foreignKeyInfo struct {
	SourceTable    string `json:"source_table" gorm:"column:source_table"`
	SourceColumn   string `json:"source_column" gorm:"column:source_column"`
	TargetTable    string `json:"target_table" gorm:"column:target_table"`
	TargetColumn   string `json:"target_column" gorm:"column:target_column"`
	ConstraintName string `json:"constraint_name" gorm:"column:constraint_name"` // 约束名：复合外键多行同名，按此聚合为一条关系
	Position       int    `json:"ordinal_position" gorm:"column:ordinal_position"`
}

type constraintInfo struct {
	TableName         string   `json:"table_name"`
	Name              string   `json:"name"`
	Kind              string   `json:"kind"`
	Columns           []string `json:"columns"`
	ReferencedTable   string   `json:"referenced_table"`
	ReferencedColumns []string `json:"referenced_columns"`
	Expression        string   `json:"expression"`
	Deferrable        bool     `json:"deferrable"`
	Validated         bool     `json:"validated"`
	OnUpdate          string   `json:"on_update"`
	OnDelete          string   `json:"on_delete"`
	Match             string   `json:"match"`
}

type indexKeyInfo struct {
	Column     string `json:"column"`
	Expression string `json:"expression"`
}

type indexInfo struct {
	TableName string         `json:"table_name"`
	Name      string         `json:"name"`
	Keys      []indexKeyInfo `json:"keys"`
	Unique    bool           `json:"unique"`
	Primary   bool           `json:"primary"`
	Invalid   bool           `json:"invalid"`
	Deferred  bool           `json:"deferred"`
	Predicate string         `json:"predicate"`
	Method    string         `json:"method"`
	Include   []string       `json:"include"`
}

type routineInfo struct {
	Schema            string                `json:"schema"`
	Name              string                `json:"name"`
	Kind              string                `json:"kind"`
	IdentityArguments string                `json:"identity_arguments"`
	Arguments         []routineArgumentInfo `json:"arguments"`
	ReturnType        string                `json:"return_type"`
	ReturnsSet        bool                  `json:"returns_set"`
	Volatility        string                `json:"volatility"`
	SecurityDefiner   bool                  `json:"security_definer"`
}

type routineArgumentInfo struct {
	Name    string `json:"name"`
	Mode    string `json:"mode"`
	Type    string `json:"type"`
	Default string `json:"default"`
}
