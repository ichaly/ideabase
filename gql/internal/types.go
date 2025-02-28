package internal

// ChainKind 表示关系链接类型
type ChainKind string

// 关系类型常量
const (
	MANY_TO_ONE  ChainKind = "many_to_one"  // 多对一关系
	ONE_TO_MANY  ChainKind = "one_to_many"  // 一对多关系
	MANY_TO_MANY ChainKind = "many_to_many" // 多对多关系
	RECURSIVE    ChainKind = "recursive"    // 递归关系
)

// Symbol 表示操作符号
type Symbol struct {
	Name  string
	Value string
	Desc  string
}

// Class 表示一个数据类/表
type Class struct {
	Name        string            // 类名
	Table       string            // 表名
	Virtual     bool              // 是否虚拟
	Fields      map[string]*Field // 字段映射
	PrimaryKeys []string          // 主键列表
	Description string            // 描述信息
}

// Field 表示一个字段
type Field struct {
	Name        string      // 字段名
	Column      string      // 列名
	Type        string      // 类型
	Virtual     bool        // 是否虚拟
	Nullable    bool        // 是否可空
	IsPrimary   bool        // 是否主键
	IsUnique    bool        // 是否唯一
	Description string      // 描述信息
	ForeignKey  *ForeignKey // 外键信息
}

// ForeignKey 表示外键关联关系
type ForeignKey struct {
	TableName  string    // 关联表名
	ColumnName string    // 关联列名
	Kind       ChainKind // 关联类型
}

// MetadataSource 元数据来源类型
type MetadataSource string

const (
	SourceDatabase MetadataSource = "database" // 数据库源
	SourceFile     MetadataSource = "file"     // 文件源
	SourceConfig   MetadataSource = "config"   // 配置源
)

// TableConfig 表示表配置
type TableConfig struct {
	// 数据类型映射
	Mapping map[string]string `mapstructure:"mapping"`

	// 元数据加载源，可以是database、file或config
	Source MetadataSource `mapstructure:"source"`

	// 数据库schema名称
	Schema string `mapstructure:"schema"`

	// 元数据缓存文件路径
	CachePath string `mapstructure:"cache-path"`

	// 是否启用缓存
	EnableCache bool `mapstructure:"enable-cache"`

	// 表名前缀（用于去除）
	TablePrefix []string `mapstructure:"table-prefix"`

	// 是否启用下划线转驼峰
	EnableCamelCase bool `mapstructure:"enable-camel-case"`

	// 要包含的表（空表示包含所有）
	IncludeTables []string `mapstructure:"include-tables"`

	// 要排除的表
	ExcludeTables []string `mapstructure:"exclude-tables"`

	// 要排除的字段
	ExcludeFields []string `mapstructure:"exclude-fields"`

	// 字段名映射（用于自定义命名）
	FieldMapping map[string]string `mapstructure:"field-mapping"`

	// 表名映射（用于自定义命名）
	TableMapping map[string]string `mapstructure:"table-mapping"`

	// 默认分页限制
	DefaultLimit int `mapstructure:"default-limit"`
}

// LoadOption 元数据加载选项
type LoadOption func() error
