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

// Class 表示一个数据类/表的完整定义
type Class struct {
	Name        string            // 类名
	Table       string            // 表名
	Virtual     bool              // 是否为虚拟类
	PrimaryKeys []string          // 主键列表
	Description string            // 描述信息
	Fields      map[string]*Field // 字段映射表(导出字段,用于序列化)
	TableNames  map[string]bool   // 表名索引 (key: 表名, value: true表示是原始表名)

	fields map[string]*Field // 内部字段索引,支持字段名和列名查询
}

// Field 表示类的一个字段
type Field struct {
	Name        string    // 字段名
	Column      string    // 列名
	Type        string    // 类型
	Virtual     bool      // 是否虚拟字段
	Nullable    bool      // 是否可空
	IsPrimary   bool      // 是否主键
	IsUnique    bool      // 是否唯一
	Description string    // 描述信息
	Relation    *Relation // 若为关系字段,指向关系定义
}

// Relation 表示类之间的关系
type Relation struct {
	SourceClass string    // 源类名
	SourceField string    // 源字段名
	TargetClass string    // 目标类名
	TargetField string    // 目标字段名
	Kind        ChainKind // 关系类型
	Reverse     *Relation // 反向关系引用
}

// AddField 添加字段到类中
func (c *Class) AddField(field *Field) {
	if c.fields == nil {
		c.fields = make(map[string]*Field)
	}
	if c.Fields == nil {
		c.Fields = make(map[string]*Field)
	}

	// 添加到导出字段映射
	c.Fields[field.Name] = field

	// 添加到内部索引
	c.fields[field.Name] = field
	if field.Column != "" && field.Column != field.Name {
		c.fields[field.Column] = field
	}
}

// GetField 获取字段定义(支持字段名或列名)
func (c *Class) GetField(nameOrColumn string) *Field {
	return c.fields[nameOrColumn]
}

// RemoveField 移除字段
func (c *Class) RemoveField(field *Field) {
	if field == nil {
		return
	}
	// 从导出字段映射中删除
	delete(c.Fields, field.Name)

	// 从内部索引中删除
	delete(c.fields, field.Name)
	if field.Column != "" && field.Column != field.Name {
		delete(c.fields, field.Column)
	}
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
