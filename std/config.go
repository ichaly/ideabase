package std

import (
	"github.com/ichaly/ideabase/std/internal"
	"github.com/spf13/viper"
)

type Config struct {
	internal.AppConfig `mapstructure:"app"`
}

func NewConfig(v *viper.Viper) (*Config, error) {
	c := &Config{}
	if err := v.Unmarshal(c); err != nil {
		return nil, err
	}
	return c, nil
}
