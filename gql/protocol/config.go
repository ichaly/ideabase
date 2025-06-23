package protocol

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

	// 默认分页限制
	DefaultLimit int `mapstructure:"default-limit"`

	// 数据类型映射
	TypeMapping map[string]string `mapstructure:"mapping"`
}

// MetadataConfig 表示元数据配置
type MetadataConfig struct {
	// 类定义映射(key: 类名)
	Classes map[string]*ClassConfig `mapstructure:"classes"`

	// 文件配置
	File string `mapstructure:"file"` // 支持 {mode} 占位符

	// 命名规范
	UseCamel    bool `mapstructure:"use-camel"`
	UseSingular bool `mapstructure:"use-singular"`

	// 关系配置
	ShowThrough bool `mapstructure:"show-through"`

	// 表名前缀（将被去除）
	TablePrefix []string `mapstructure:"table-prefix"`

	// 要排除的表
	ExcludeTables []string `mapstructure:"exclude-tables"`

	// 要排除的字段
	ExcludeFields []string `mapstructure:"exclude-fields"`
}

// ClassConfig 表示类配置
type ClassConfig struct {
	// 表名 (对应数据库表)
	Table string `mapstructure:"table"`

	// 描述
	Description string `mapstructure:"description"`

	// 主键列表
	PrimaryKeys []string `mapstructure:"primary_keys"`

	// 类级别自定义Resolver
	Resolver string `mapstructure:"resolver"`

	// 字段定义 (使用字段名作为键)
	Fields map[string]*FieldConfig `mapstructure:"fields"`

	// 关系定义
	Relations []RelationConfig `mapstructure:"relations"`

	// 字段过滤配置
	ExcludeFields []string `mapstructure:"exclude_fields"` // 排除这些字段
	IncludeFields []string `mapstructure:"include_fields"` // 仅包含这些字段

	// override: true 表示别名覆盖主类指针，false（默认）为附加模式
	Override bool `mapstructure:"override"`
}

// FieldConfig 表示字段配置
type FieldConfig struct {
	// 列名 (数据库中的列名)
	Column string `mapstructure:"column"`

	// 数据类型
	Type string `mapstructure:"type"`

	// 描述
	Description string `mapstructure:"description"`

	// 字段特性
	IsPrimary  bool `mapstructure:"primary"`
	IsNullable bool `mapstructure:"nullable"`
	IsUnique   bool `mapstructure:"unique"`

	// 默认值
	DefaultValue string `mapstructure:"default_value"`

	// 字段级别自定义Resolver
	Resolver string `mapstructure:"resolver"`

	// 关系配置
	Relation *RelationConfig `mapstructure:"relation"`

	// override: true 表示字段别名覆盖主字段指针，false（默认）为附加模式
	Override bool `mapstructure:"override"`
}

// RelationConfig 表示关系配置
type RelationConfig struct {
	// 关系定义
	SourceClass string `mapstructure:"source_class"`
	SourceField string `mapstructure:"source_field"`
	TargetClass string `mapstructure:"target_class"`
	TargetField string `mapstructure:"target_field"`
	Type        string `mapstructure:"type"`

	// 多对多关系中间表配置
	Through *ThroughConfig `mapstructure:"through,omitempty"`
}

// ThroughConfig 表示多对多关系中的中间表配置
type ThroughConfig struct {
	// 中间表名称
	TableName string `mapstructure:"table_name"`

	// 中间表中指向源表的外键
	SourceKey string `mapstructure:"source_key"`

	// 中间表中指向目标表的外键
	TargetKey string `mapstructure:"target_key"`
}
