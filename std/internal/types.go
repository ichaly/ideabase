package internal

// AppConfig 应用程序配置信息
type AppConfig struct {
	Name       string      `mapstructure:"name"`        // 应用名称
	Port       string      `mapstructure:"port"`        // 服务端口
	Root       string      `mapstructure:"root"`        // 应用根目录路径
	Cache      *DataSource `mapstructure:"cache"`       // 缓存数据源配置
	Database   *DataSource `mapstructure:"database"`    // 数据库配置信息
	EncryptKey string      `mapstructure:"encrypt-key"` // Cookie加密密钥
}

// DataSource 数据源配置信息
type DataSource struct {
	Uri      string       `mapstructure:"uri"`      // 完整连接URI
	Host     string       `mapstructure:"host"`     // 数据库主机地址
	Port     int          `mapstructure:"port"`     // 数据库端口
	Name     string       `mapstructure:"name"`     // 数据库名称（如数据库名）
	Dialect  string       `mapstructure:"dialect"`  // 数据库方言（如mysql、postgres）
	Username string       `mapstructure:"username"` // 访问账号
	Password string       `mapstructure:"password"` // 访问密码
	Sources  []DataSource `mapstructure:"sources"`  // 主数据源列表（用于分片/集群）
	Replicas []DataSource `mapstructure:"replicas"` // 副本数据源列表（用于读写分离）
}
