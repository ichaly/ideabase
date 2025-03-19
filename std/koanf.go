package std

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ichaly/ideabase/utl"
	"github.com/joho/godotenv"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// KoanfOption 定义配置选项函数类型
type KoanfOption func(*koanfOptions)

// koanfOptions 保存koanf的配置选项
type koanfOptions struct {
	configType string
	envPrefix  string
	delim      string
	strict     bool
}

// NewKoanf 创建新的配置实例
func NewKoanf(filePath string, opts ...KoanfOption) (*koanf.Koanf, error) {
	if filePath == "" {
		return nil, errors.New("配置文件路径不能为空")
	}

	// 解析文件路径和名称
	path := filepath.Dir(filePath)
	ext := filepath.Ext(filePath)
	name := strings.TrimSuffix(filepath.Base(filePath), ext)

	// 初始化选项
	options := &koanfOptions{
		configType: strings.TrimPrefix(ext, "."),
		envPrefix:  "APP",
		delim:      ".",
	}

	// 应用自定义配置选项
	for _, opt := range opts {
		opt(options)
	}

	// 初始化koanf
	k := koanf.NewWithConf(koanf.Conf{
		Delim:       options.delim,
		StrictMerge: options.strict,
	})

	// 设置基础配置（不包含环境变量）
	k.Set("mode", "dev")
	k.Set("profiles.active", "")
	k.Set("app.root", filepath.Dir(utl.Root()))

	// 加载环境变量文件(可选)
	if err := loadEnvFile(); err != nil {
		return nil, fmt.Errorf("加载环境变量文件: %w", err)
	}

	// 加载主配置文件
	if err := loadConfigFile(k, filePath, options); err != nil {
		return nil, fmt.Errorf("加载配置文件: %w", err)
	}

	// 合并profile配置
	if err := mergeProfiles(k, path, name, ext, options); err != nil {
		return nil, fmt.Errorf("合并环境配置: %w", err)
	}

	// 最后加载环境变量，确保环境变量优先级最高
	envProvider := env.Provider(options.envPrefix+"_", options.delim, func(s string) string {
		return strings.Replace(strings.ToLower(strings.TrimPrefix(s, options.envPrefix+"_")), "_", options.delim, -1)
	})

	if err := k.Load(envProvider, nil); err != nil {
		return nil, fmt.Errorf("加载环境变量失败: %w", err)
	}

	return k, nil
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
		return fmt.Errorf("加载.env文件失败: %w", err)
	}

	return nil
}

// loadConfigFile 加载配置文件
func loadConfigFile(k *koanf.Koanf, filePath string, options *koanfOptions) error {
	// 根据文件类型选择合适的解析器
	var parser koanf.Parser

	switch options.configType {
	case "yaml", "yml":
		parser = yaml.Parser()
	// 可以根据需要添加其他格式的解析器
	default:
		return fmt.Errorf("不支持的配置文件类型: %s", options.configType)
	}

	// 加载配置文件
	if err := k.Load(file.Provider(filePath), parser); err != nil {
		return fmt.Errorf("加载配置文件失败: %w", err)
	}

	return nil
}

// getActiveProfiles 获取激活的profiles
func getActiveProfiles(k *koanf.Koanf) []string {
	var profiles []string

	// 添加profiles.active中指定的profiles
	activeProfiles := strings.Split(k.String("profiles.active"), ",")
	for _, p := range activeProfiles {
		if p = strings.TrimSpace(p); p != "" {
			profiles = append(profiles, p)
		}
	}

	// 添加mode作为profile
	if mode := k.String("mode"); mode != "" {
		profiles = append(profiles, mode)
	}

	return profiles
}

// mergeProfiles 合并profile配置文件
func mergeProfiles(k *koanf.Koanf, path, name, ext string, options *koanfOptions) error {
	// 获取激活的profiles
	profiles := getActiveProfiles(k)

	// 合并每个profile的配置
	for _, profile := range profiles {
		if profile == "" {
			continue
		}

		// 构建profile配置文件路径
		profileFilePath := filepath.Join(path, utl.JoinString(name, "-", profile, ext))

		// 检查文件是否存在
		if _, err := os.Stat(profileFilePath); os.IsNotExist(err) {
			// profile配置文件不存在,跳过加载
			continue
		}

		// 根据文件类型选择合适的解析器
		var parser koanf.Parser
		switch options.configType {
		case "yaml", "yml":
			parser = yaml.Parser()
		// 可以根据需要添加其他格式的解析器
		default:
			return fmt.Errorf("不支持的配置文件类型: %s", options.configType)
		}

		// 合并profile配置
		if err := k.Load(file.Provider(profileFilePath), parser); err != nil {
			return fmt.Errorf("合并profile配置文件失败: %w", err)
		}
	}

	return nil
}

// WithConfigType 设置配置文件类型
func WithConfigType(configType string) KoanfOption {
	return func(options *koanfOptions) {
		options.configType = configType
	}
}

// WithEnvPrefix 设置环境变量前缀
func WithEnvPrefix(prefix string) KoanfOption {
	return func(options *koanfOptions) {
		options.envPrefix = prefix
	}
}

// WithDelimiter 设置配置项分隔符
func WithDelimiter(delim string) KoanfOption {
	return func(options *koanfOptions) {
		options.delim = delim
	}
}

// WithStrictMerge 设置严格合并
func WithStrictMerge(strict bool) KoanfOption {
	return func(options *koanfOptions) {
		options.strict = strict
	}
}
