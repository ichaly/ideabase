package std

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ichaly/ideabase/utl"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

// ConfigOption 定义配置选项函数类型
type ConfigOption func(*viper.Viper)

// NewViper 创建新的配置实例
func NewViper(file string, opts ...ConfigOption) (*viper.Viper, error) {
	if file == "" {
		return nil, errors.New("config file path cannot be empty")
	}

	// 解析文件路径和名称
	path := filepath.Dir(file)
	name := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))

	// 初始化配置
	v := viper.New()

	// 应用自定义配置选项
	for _, opt := range opts {
		opt(v)
	}

	// 设置基础配置
	if err := setupBaseConfig(v, path, name); err != nil {
		return nil, fmt.Errorf("setup base config: %w", err)
	}

	// 加载环境变量
	if err := loadEnvFile(); err != nil {
		return nil, fmt.Errorf("load env file: %w", err)
	}

	// 加载主配置文件
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	// 合并profile配置
	if err := mergeProfiles(v, name); err != nil {
		return nil, fmt.Errorf("merge profiles: %w", err)
	}

	// 开启配置文件变更监听
	v.WatchConfig()

	return v, nil
}

// 设置基础配置
func setupBaseConfig(v *viper.Viper, path, name string) error {
	v.SetConfigName(name)
	v.AddConfigPath(path)

	// 支持环境变量自动替换
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	// 设置默认的环境变量前缀,如果已经通过 WithEnvPrefix 设置则不会覆盖
	if v.GetEnvPrefix() == "" {
		v.SetEnvPrefix("app")
	}
	v.AutomaticEnv()

	// 设置默认值
	v.SetDefault("mode", "dev")
	v.SetDefault("profiles.active", "")

	return nil
}

// loadEnvFile 加载环境变量文件(可选)
func loadEnvFile() error {
	envFile := filepath.Join(utl.Root(), ".env")

	// 检查文件是否存在
	if _, err := os.Stat(envFile); os.IsNotExist(err) {
		// .env 文件不存在,跳过加载
		return nil
	}

	// 加载 .env 文件
	if err := godotenv.Load(envFile); err != nil {
		return fmt.Errorf("load .env file: %w", err)
	}

	return nil
}

// 合并profile配置文件
func mergeProfiles(v *viper.Viper, name string) error {
	// 获取激活的profiles
	profiles := getActiveProfiles(v)

	// 合并每个profile的配置
	for _, profile := range profiles {
		if err := mergeProfileConfig(v, name, profile); err != nil {
			return fmt.Errorf("merge profile %s: %w", profile, err)
		}
	}

	return nil
}

// 获取激活的profiles
func getActiveProfiles(v *viper.Viper) []string {
	var profiles []string

	// 添加profiles.active中指定的profiles
	activeProfiles := strings.Split(v.GetString("profiles.active"), ",")
	for _, p := range activeProfiles {
		if p = strings.TrimSpace(p); p != "" {
			profiles = append(profiles, p)
		}
	}

	// 添加mode作为profile
	if mode := v.GetString("mode"); mode != "" {
		profiles = append(profiles, mode)
	}

	return profiles
}

// 合并单个profile配置
func mergeProfileConfig(v *viper.Viper, name, profile string) error {
	if profile == "" {
		return nil
	}
	v.SetConfigName(utl.JoinString(name, "-", profile))
	if err := v.MergeInConfig(); err != nil && !errors.As(err, &viper.ConfigFileNotFoundError{}) {
		return fmt.Errorf("merge config file: %w", err)
	}
	return nil
}

// WithConfigType 设置配置文件类型
func WithConfigType(configType string) ConfigOption {
	return func(v *viper.Viper) {
		v.SetConfigType(configType)
	}
}

// WithEnvPrefix 设置环境变量前缀
func WithEnvPrefix(prefix string) ConfigOption {
	return func(v *viper.Viper) {
		v.SetEnvPrefix(prefix)
	}
}
