package internal

import "github.com/vektah/gqlparser/v2/ast"

type TableConfig struct {
	Tables       []TableDefinition `mapstructure:"tables"`
	Mapping      map[string]string `mapstructure:"mapping"`
	UseCamel     bool              `mapstructure:"use-camel"`
	Prefixes     []string          `mapstructure:"prefixes"`
	BlockList    []string          `mapstructure:"block-list"`
	DefaultLimit int               `mapstructure:"default-limit"`
}

type TableDefinition struct {
	Name    string             `mapstructure:"name"`
	Type    string             `mapstructure:"type"`
	Table   string             `mapstructure:"table"`
	Columns []ColumnDefinition `mapstructure:"columns"`
}

type ColumnDefinition struct {
	Name      string `mapstructure:"name"`
	Type      string `mapstructure:"type"`
	RelatedTo string `mapstructure:"related-to"`
}

type ChainKind string

type Symbol struct {
	Name     string
	Text     string
	Describe string
}

type LoadOption func() error

type NamedOption func(table, column string) string

type Entry struct {
	DataType          string `gorm:"column:data_type;"`
	Nullable          bool   `gorm:"column:is_nullable;"`
	Iterable          bool   `gorm:"column:is_iterable;"`
	IsPrimary         bool   `gorm:"column:is_primary;"`
	IsForeign         bool   `gorm:"column:is_foreign;"`
	TableName         string `gorm:"column:table_name;"`
	ColumnName        string `gorm:"column:column_name;"`
	TableRelation     string `gorm:"column:table_relation;"`
	ColumnRelation    string `gorm:"column:column_relation;"`
	TableDescription  string `gorm:"column:table_description;"`
	ColumnDescription string `gorm:"column:column_description;"`
}

type Class struct {
	Kind        ast.DefinitionKind
	Name        string
	Table       string
	Fields      map[string]*Field
	Primary     []string
	Virtual     bool
	Description string
}

type Field struct {
	Kind        ChainKind
	Type        *ast.Type
	Name        string
	Link        *Entry
	Join        *Entry
	Table       string
	Column      string
	Virtual     bool
	DataType    string
	Arguments   []*Input
	Description string
}

type Input struct {
	Type        *ast.Type
	Name        string
	Default     string
	Description string
}

type Value struct {
}
