package std

import (
	"github.com/ichaly/ideabase/std/internal"
)

// Config 表示标准配置
type Config struct {
	internal.AppConfig `mapstructure:"app"`
	Mode               string `mapstructure:"mode"`
}

func NewConfig(k *Konfig) (*Config, error) {
	c := &Config{}
	if err := k.Unmarshal(&c); err != nil {
		return nil, err
	}
	return c, nil
}
