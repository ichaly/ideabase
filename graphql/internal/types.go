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
	Primary     map[string]*Field
	Foreign     map[string]*Field
	Virtual     bool
	Description string
}

type Field struct {
	Name         string
	Type         *ast.Type
	Kind         ChainKind
	Link         *Entry
	Join         *Entry
	Table        string
	Column       string
	Virtual      bool
	Arguments    []*Input
	Description  string
	RelationKind string
}

type Input struct {
	Name        string
	Type        *ast.Type
	Default     string
	Description string
}

type Chain struct {
}

type Value struct {
}
