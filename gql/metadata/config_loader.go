package metadata

import (
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/spf13/viper"
)

// 配置文件中元数据的结构
type configMetadata struct {
	Tables []*configTable `mapstructure:"tables"`
}

// 配置文件中表的结构
type configTable struct {
	Name        string          `mapstructure:"name"`
	DisplayName string          `mapstructure:"display_name"`
	Description string          `mapstructure:"description"`
	PrimaryKeys []string        `mapstructure:"primary_keys"`
	Columns     []*configColumn `mapstructure:"columns"`
}

// 配置文件中列的结构
type configColumn struct {
	Name        string            `mapstructure:"name"`
	DisplayName string            `mapstructure:"display_name"`
	Type        string            `mapstructure:"type"`
	Description string            `mapstructure:"description"`
	Nullable    bool              `mapstructure:"nullable"`
	IsPrimary   bool              `mapstructure:"is_primary"`
	IsUnique    bool              `mapstructure:"is_unique"`
	ForeignKey  *configForeignKey `mapstructure:"foreign_key"`
}

// 配置文件中外键的结构
type configForeignKey struct {
	Table  string `mapstructure:"table"`
	Column string `mapstructure:"column"`
	Kind   string `mapstructure:"kind"`
}

// ConfigLoader 配置元数据加载器
type ConfigLoader struct {
	config *viper.Viper
}

// NewConfigLoader 创建新的配置加载器
func NewConfigLoader(config *viper.Viper) *ConfigLoader {
	return &ConfigLoader{
		config: config,
	}
}

// LoadMetadata 从配置加载元数据
func (l *ConfigLoader) LoadMetadata() (map[string]*internal.Class, map[string]map[string]*internal.ForeignKey, error) {
	// 创建结果容器
	classes := make(map[string]*internal.Class)
	relationships := make(map[string]map[string]*internal.ForeignKey)

	// 从配置读取元数据
	var metadata configMetadata
	if err := l.config.UnmarshalKey("metadata", &metadata); err != nil {
		return nil, nil, err
	}

	// 处理所有表
	for _, table := range metadata.Tables {
		className := table.DisplayName
		if className == "" {
			className = table.Name
		}

		// 创建类
		class := &internal.Class{
			Name:        className,
			Table:       table.Name,
			Virtual:     false,
			Fields:      make(map[string]*internal.Field),
			PrimaryKeys: table.PrimaryKeys,
			Description: table.Description,
		}
		classes[className] = class

		// 处理所有列
		for _, column := range table.Columns {
			fieldName := column.DisplayName
			if fieldName == "" {
				fieldName = column.Name
			}

			// 创建字段
			field := &internal.Field{
				Name:        fieldName,
				Column:      column.Name,
				Type:        column.Type,
				Virtual:     false,
				Nullable:    column.Nullable,
				IsPrimary:   column.IsPrimary,
				IsUnique:    column.IsUnique,
				Description: column.Description,
			}
			class.Fields[fieldName] = field

			// 处理外键
			if column.ForeignKey != nil {
				// 创建外键对象
				foreignKey := &internal.ForeignKey{
					TableName:  column.ForeignKey.Table,
					ColumnName: column.ForeignKey.Column,
				}

				// 设置关联类型
				switch column.ForeignKey.Kind {
				case "one_to_many":
					foreignKey.Kind = internal.ONE_TO_MANY
				case "many_to_one":
					foreignKey.Kind = internal.MANY_TO_ONE
				case "many_to_many":
					foreignKey.Kind = internal.MANY_TO_MANY
				case "recursive":
					foreignKey.Kind = internal.RECURSIVE
				default:
					foreignKey.Kind = internal.MANY_TO_ONE // 默认为多对一
				}

				// 设置字段外键
				field.ForeignKey = foreignKey

				// 添加到关系映射
				if _, ok := relationships[table.Name]; !ok {
					relationships[table.Name] = make(map[string]*internal.ForeignKey)
				}
				relationships[table.Name][column.Name] = foreignKey
			}
		}
	}

	return classes, relationships, nil
}
