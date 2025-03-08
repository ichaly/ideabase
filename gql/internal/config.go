package internal

import "github.com/ichaly/ideabase/std"

// Config 表示GraphQL配置
type Config struct {
	std.Config `mapstructure:",squash"`
	Schema     SchemaConfig   `mapstructure:"schema"`
	Metadata   MetadataConfig `mapstructure:"metadata"`
}

// SchemaConfig 表示Schema相关配置
type SchemaConfig struct {
	// 数据库schema
	Schema string `mapstructure:"schema"`

	// 是否启用驼峰命名
	EnableCamelCase bool `mapstructure:"enable-camel-case"`

	// 表名前缀（用于去除）
	TablePrefix []string `mapstructure:"table-prefix"`

	// 要包含的表（空表示包含所有）
	IncludeTables []string `mapstructure:"include-tables"`

	// 要排除的表
	ExcludeTables []string `mapstructure:"exclude-tables"`

	// 要排除的字段
	ExcludeFields []string `mapstructure:"exclude-fields"`

	// 默认分页限制
	DefaultLimit int `mapstructure:"default-limit"`

	// 字段名映射（用于自定义命名）
	FieldMapping map[string]string `mapstructure:"field-mapping"`

	// 表名映射（用于自定义命名）
	TableMapping map[string]string `mapstructure:"table-mapping"`

	// 数据类型映射
	TypeMapping map[string]string `mapstructure:"type-mapping"`
}

// MetadataConfig 表示元数据配置
type MetadataConfig struct {
	// 表定义映射(key: 原始表名)
	Tables map[string]*TableConfig `mapstructure:"tables"`
}

// TableConfig 表示表配置
type TableConfig struct {
	// 转换后的表名
	Name string `mapstructure:"name"`

	// 描述
	Description string `mapstructure:"description"`

	// 主键列表
	PrimaryKeys []string `mapstructure:"primary_keys"`

	// 列定义映射(key: 原始列名)
	Columns map[string]*ColumnConfig `mapstructure:"columns"`

	// 是否虚拟表
	Virtual bool `mapstructure:"virtual"`

	// 表级别的关系定义
	Relations []RelationConfig `mapstructure:"relations"`
}

// ColumnConfig 表示列配置
type ColumnConfig struct {
	// 转换后的字段名
	Name string `mapstructure:"name"`

	// 数据类型
	Type string `mapstructure:"type"`

	// 描述
	Description string `mapstructure:"description"`

	// 是否主键
	IsPrimary bool `mapstructure:"is_primary"`

	// 是否可空
	IsNullable bool `mapstructure:"is_nullable"`

	// 是否唯一
	IsUnique bool `mapstructure:"is_unique"`

	// 是否虚拟字段
	Virtual bool `mapstructure:"virtual"`

	// 默认值
	DefaultValue string `mapstructure:"default_value"`

	// 字段级别的关系定义
	Relation *RelationConfig `mapstructure:"relation"`
}

// RelationConfig 表示关系配置
type RelationConfig struct {
	// 源类名
	SourceClass string `mapstructure:"source_class"`

	// 源字段名
	SourceField string `mapstructure:"source_field"`

	// 目标类名
	TargetClass string `mapstructure:"targetClass,target_class"`

	// 目标字段名
	TargetField string `mapstructure:"targetField,target_field"`

	// 关系类型: many_to_one, one_to_many, many_to_many, recursive
	Type string `mapstructure:"type"`

	// 多对多关系配置
	Through *ThroughConfig `mapstructure:"through,omitempty"`
}

// ThroughConfig 表示多对多关系中的中间表配置
type ThroughConfig struct {
	// 中间表名称
	Table string `mapstructure:"table"`

	// 中间表中指向源表的外键
	SourceKey string `mapstructure:"sourceKey,source_key"`

	// 中间表中指向目标表的外键
	TargetKey string `mapstructure:"targetKey,target_key"`
}
