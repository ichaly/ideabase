package graphql

import (
	"embed"
	"github.com/ichaly/ideabase/graphql/internal"
	"github.com/ichaly/ideabase/starter"
	"github.com/ichaly/ideabase/utility"
	"github.com/jinzhu/inflection"
	"github.com/spf13/viper"
	"gorm.io/gorm"
	"strings"
	"text/template"
)

//go:embed assets/tpl/*
var templates embed.FS

//go:embed assets/sql/pgsql.sql
var pgsql string

func init() {
	inflection.AddUncountable("children")
}

type Config struct {
	starter.Config       `mapstructure:",squash"`
	internal.TableConfig `mapstructure:"schema"`
}

type Metadata struct {
	db  *gorm.DB
	cfg *Config
	tpl *template.Template

	Nodes utility.AnyMap[*internal.Class]
}

func NewMetadata(v *viper.Viper, d *gorm.DB) (*Metadata, error) {
	//初始化模板
	tpl, err := template.ParseFS(templates, "assets/tpl/*.tpl")
	if err != nil {
		return nil, err
	}

	//初始化配置
	cfg := &Config{TableConfig: internal.TableConfig{Mapping: dataTypes}}
	v.SetDefault("schema.default-limit", 10)
	if err = v.Unmarshal(cfg); err != nil {
		return nil, err
	}

	my := &Metadata{
		db: d, cfg: cfg, tpl: tpl,
		Nodes: make(utility.AnyMap[*internal.Class]),
	}

	for _, o := range []internal.LoadOption{
		my.expressions,
		my.tableOption,
		my.orderOption,
		my.whereOption,
		my.inputOption,
		my.entryOption,
	} {
		if err := o(); err != nil {
			return nil, err
		}
	}

	return my, nil
}

func (my *Metadata) Marshal() (string, error) {
	var w strings.Builder
	if err := my.tpl.ExecuteTemplate(&w, "build.tpl", my.Nodes); err != nil {
		return "", err
	}
	return w.String(), nil
}

func (my *Metadata) FindClass(className string, virtual bool) (*internal.Class, bool) {
	class, ok := my.Nodes[className]
	if !ok || class.Virtual != virtual {
		return nil, false
	}
	return class, true
}

func (my *Metadata) FindField(className, fieldName string, virtual bool) (*internal.Field, bool) {
	class, ok := my.Nodes[className]
	if !ok || class.Virtual != virtual {
		return nil, false
	}
	field, ok := class.Fields[fieldName]
	if !ok || field.Virtual != virtual {
		return nil, false
	}
	return field, true
}

func (my *Metadata) TableName(className string, virtual bool) (string, bool) {
	class, ok := my.Nodes[className]
	if !ok || class.Virtual != virtual {
		return "", false
	}
	return class.Table, len(class.Table) > 0
}

func (my *Metadata) ColumnName(className, fieldName string, virtual bool) (string, bool) {
	class, ok := my.Nodes[className]
	if !ok || class.Virtual != virtual {
		return "", false
	}
	field, ok := class.Fields[fieldName]
	if !ok || field.Virtual != virtual {
		return "", false
	}
	return field.Column, len(field.Column) > 0
}
