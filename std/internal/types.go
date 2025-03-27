package internal

type AppConfig struct {
	Name     string      `mapstructure:"name"`
	Port     string      `mapstructure:"port"`
	Host     string      `mapstructure:"host"`
	Root     string      `mapstructure:"root"`
	Cache    *DataSource `mapstructure:"cache"`
	Database *DataSource `mapstructure:"database"`
}

type DataSource struct {
	Uri      string       `mapstructure:"uri"`
	Host     string       `mapstructure:"host"`
	Port     int          `mapstructure:"port"`
	Name     string       `mapstructure:"name"`
	Dialect  string       `mapstructure:"dialect"`
	Username string       `mapstructure:"username"`
	Password string       `mapstructure:"password"`
	Sources  []DataSource `mapstructure:"sources"`
	Replicas []DataSource `mapstructure:"replicas"`
}
