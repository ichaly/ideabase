package internal

type AppConfig struct {
	Name  string `mapstructure:"name"`
	Port  string `mapstructure:"port"`
	Host  string `mapstructure:"host"`
	Root  string `mapstructure:"root"`
	Debug bool   `mapstructure:"debug"`
}

type DataSource struct {
	Url      string       `mapstructure:"url"`
	Host     string       `mapstructure:"host"`
	Port     int          `mapstructure:"port"`
	Name     string       `mapstructure:"name"`
	Dialect  string       `mapstructure:"dialect"`
	Username string       `mapstructure:"username"`
	Password string       `mapstructure:"password"`
	Sources  []DataSource `mapstructure:"sources"`
	Replicas []DataSource `mapstructure:"replicas"`
}

type CacheConfig struct {
	DataSource
}

type DatabaseConfig struct {
	AppConfig  `mapstructure:"app"`
	DataSource `mapstructure:"database"`
}
