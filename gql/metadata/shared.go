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
	TableName        string `json:"table_name" gorm:"column:table_name"`
	TableDescription string `json:"table_description" gorm:"column:table_description"`
}

type columnInfo struct {
	TableName         string       `json:"table_name" gorm:"column:table_name"`
	ColumnName        string       `json:"column_name" gorm:"column:column_name"`
	DataType          string       `json:"data_type" gorm:"column:data_type"`
	IsNullable        NullableType `json:"is_nullable" gorm:"column:is_nullable"`
	CharMaxLength     *int64       `json:"character_maximum_length" gorm:"column:character_maximum_length"`
	NumericPrecision  *int64       `json:"numeric_precision" gorm:"column:numeric_precision"`
	NumericScale      *int64       `json:"numeric_scale" gorm:"column:numeric_scale"`
	ColumnDescription string       `json:"column_description" gorm:"column:column_description"`
}

type primaryKeyInfo struct {
	TableName  string `json:"table_name" gorm:"column:table_name"`
	ColumnName string `json:"column_name" gorm:"column:column_name"`
}

type foreignKeyInfo struct {
	SourceTable  string `json:"source_table" gorm:"column:source_table"`
	SourceColumn string `json:"source_column" gorm:"column:source_column"`
	TargetTable  string `json:"target_table" gorm:"column:target_table"`
	TargetColumn string `json:"target_column" gorm:"column:target_column"`
}
