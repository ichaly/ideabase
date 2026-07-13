package internal

import "github.com/ichaly/ideabase/std"

// Config 表示GraphQL配置
type Config struct {
	std.Config   `mapstructure:",squash"`
	Schema       SchemaConfig       `mapstructure:"schema"`
	Metadata     MetadataConfig     `mapstructure:"metadata"`
	Subscription SubscriptionConfig `mapstructure:"subscription"`
	Search       SearchConfig       `mapstructure:"search"`
}

// SearchConfig 全文搜索配置；缺省启动时自动探测数据库能力
type SearchConfig struct {
	// 模式：tsvector(分词检索) / trigram(pg_trgm) / ilike(降级)，空=自动探测
	Mode string `mapstructure:"mode"`

	// tsvector模式的text search配置名（如jiebacfg），空=自动探测中文分词配置
	Config string `mapstructure:"config"`
}

// SubscriptionConfig 表示订阅(CDC)相关配置
type SubscriptionConfig struct {
	// 逻辑复制发布名，缺省 ideabase_cdc（不存在时引擎自动创建 FOR ALL TABLES）
	Publication string `mapstructure:"publication"`

	// 复制连接DSN，缺省复用主连接DSN（需要REPLICATION权限）
	DSN string `mapstructure:"dsn"`
}

// SchemaConfig 表示Schema相关配置
type SchemaConfig struct {
	// 数据库schema
	Schema string `mapstructure:"schema"`

	// GraphQL schema文件路径（配置后优先从文件加载，生产环境推荐）
	File string `mapstructure:"file"`

	// 默认分页限制
	DefaultLimit int `mapstructure:"default-limit"`

	// 查询选择集最大嵌套深度（0=不限制）
	MaxDepth int `mapstructure:"max-depth"`

	// 是否允许自省查询（生产对外暴露时可关闭）
	Introspection bool `mapstructure:"introspection"`

	// HTTP边界只接受持久化操作（operationName查已注册文档），拒绝原始查询文本
	PersistedOnly bool `mapstructure:"persisted-only"`

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

	// 仅包含的表（白名单，支持尾部*通配，如 bot_*；非空时未命中的表一律排除，排除规则优先）
	IncludeTables []string `mapstructure:"include-tables"`

	// 要排除的表（支持尾部*通配）
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

	// 主键生成策略：database（数据库自增/UUID默认值）、snowflake或WithIDGenerator注册名
	IDGenerator string `mapstructure:"id-generator"`

	// 字段定义 (使用字段名作为键)
	Fields map[string]*FieldConfig `mapstructure:"fields"`

	// 参与全文搜索的字段
	Search []string `mapstructure:"search"`

	// 行级作用域：编译期强制注入的过滤（租户/属主隔离），值由 gql.WithScope 在请求上下文注入
	Scope []ScopeConfig `mapstructure:"scope"`

	// 字段过滤配置
	ExcludeFields []string `mapstructure:"exclude_fields"` // 排除这些字段
	IncludeFields []string `mapstructure:"include_fields"` // 仅包含这些字段

	// override: true 表示别名覆盖主类指针，false（默认）为附加模式
	Override bool `mapstructure:"override"`
}

// ScopeConfig 行级作用域规则配置
type ScopeConfig struct {
	Column  string `mapstructure:"column"`  // 数据库列名（如 tenant_id / user_id）
	Context string `mapstructure:"context"` // 上下文键名（如 tenant / userId）
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

	// 复合外键列组（与目标列组按序对齐，声明后覆盖单列字段）
	SourceFields []string `mapstructure:"source_fields"`
	TargetFields []string `mapstructure:"target_fields"`

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
