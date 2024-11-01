package starter

import (
	"github.com/ichaly/ideabase/utility"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
	"path/filepath"
	"strings"
)

func NewViper(file string) (*viper.Viper, error) {
	//解析文件路径和名称
	path := filepath.Dir(file)
	name := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))

	//加载环境变量
	if err := godotenv.Load(filepath.Join(utility.Root(), ".env")); err != nil {
		return nil, err
	}

	//初始化配置
	v := viper.New()
	v.SetConfigName(name)
	v.AddConfigPath(path)

	//支持环境变量自动替换
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.SetEnvPrefix("app")
	v.AutomaticEnv()

	v.SetDefault("mode", "dev")

	//读取跟配置文件
	if err := v.ReadInConfig(); err != nil {
		return nil, err
	}

	//合并其他配置文件
	profiles := strings.Split(v.GetString("profiles.active"), ",")
	profiles = append(profiles, v.GetString("mode"))

	for _, p := range profiles {
		if len(p) == 0 {
			continue
		}
		file = utility.JoinString(name, "-", p)
		v.SetConfigName(file)
		_ = v.MergeInConfig()
	}

	//开启配置文件变更监听
	v.WatchConfig()

	return v, nil
}
