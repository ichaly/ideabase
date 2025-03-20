package std

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/ichaly/ideabase/utl"
	"github.com/joho/godotenv"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Konfig 配置管理器，包装了koanf.Koanf
type Konfig struct {
	k       *koanf.Koanf      // 底层koanf实例
	options *konfigOptions    // 配置选项
	watcher *fsnotify.Watcher // 文件监视器
}

// KonfigOption 定义配置选项函数类型
type KonfigOption func(*konfigOptions)

// koanfOptions 保存koanf的配置选项
type konfigOptions struct {
	configType string
	envPrefix  string
	filePath   string
	delim      string
	strict     bool
}

// WithFilePath 设置配置文件路径
func WithFilePath(filePath string) KonfigOption {
	return func(options *konfigOptions) {
		// 如果提供了文件路径，解析文件类型
		if filePath != "" {
			options.filePath = filePath
			ext := filepath.Ext(filePath)
			options.configType = strings.TrimPrefix(ext, ".")
		}
	}
}

// WithConfigType 设置配置文件类型
func WithConfigType(configType string) KonfigOption {
	return func(options *konfigOptions) {
		options.configType = configType
	}
}

// WithEnvPrefix 设置环境变量前缀
func WithEnvPrefix(prefix string) KonfigOption {
	return func(options *konfigOptions) {
		options.envPrefix = prefix
	}
}

// WithDelimiter 设置配置项分隔符
func WithDelimiter(delim string) KonfigOption {
	return func(options *konfigOptions) {
		options.delim = delim
	}
}

// WithStrictMerge 设置严格合并
func WithStrictMerge(strict bool) KonfigOption {
	return func(options *konfigOptions) {
		options.strict = strict
	}
}

// NewKonfig 创建新的配置管理器，实现与原有NewKoanf相同的功能
func NewKonfig(opts ...KonfigOption) (*Konfig, error) {
	// 初始化选项
	options := &konfigOptions{
		configType: "yaml", // 默认使用yaml
		envPrefix:  "APP",
		delim:      ".",
	}
	// 应用自定义配置选项
	for _, opt := range opts {
		opt(options)
	}

	// 创建默认koanf实例
	k := koanf.NewWithConf(koanf.Conf{
		Delim:       options.delim,
		StrictMerge: options.strict,
	})

	// 设置基础配置
	k.Set("mode", "dev")
	k.Set("profiles.active", "")
	k.Set("app.root", filepath.Dir(utl.Root()))

	// 创建Konfig实例
	konfig := &Konfig{
		k:       k,
		options: options,
	}

	// 加载环境变量文件(可选)
	if err := loadEnvFile(); err != nil {
		return nil, fmt.Errorf("加载环境变量文件: %w", err)
	}

	// 如果提供了配置文件路径，加载配置文件
	if options.filePath != "" {
		// 加载主配置文件
		if err := loadConfigFile(k, options.filePath, options); err != nil {
			return nil, fmt.Errorf("加载配置文件: %w", err)
		}

		// 解析文件路径和名称用于profile配置
		path := filepath.Dir(options.filePath)
		ext := filepath.Ext(options.filePath)
		name := strings.TrimSuffix(filepath.Base(options.filePath), ext)

		// 合并profile配置
		if err := mergeProfiles(k, path, name, options); err != nil {
			return nil, fmt.Errorf("合并环境配置: %w", err)
		}
	}

	// 最后加载环境变量，确保环境变量优先级最高
	envProvider := env.Provider(options.envPrefix+"_", options.delim, func(s string) string {
		return strings.Replace(strings.ToLower(strings.TrimPrefix(s, options.envPrefix+"_")), "_", options.delim, -1)
	})

	if err := k.Load(envProvider, nil); err != nil {
		return nil, fmt.Errorf("加载环境变量失败: %w", err)
	}

	return konfig, nil
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
func loadConfigFile(k *koanf.Koanf, filePath string, options *konfigOptions) error {
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

// mergeProfiles 合并profile配置文件
func mergeProfiles(k *koanf.Koanf, path, name string, options *konfigOptions) error {
	// 获取激活的profiles
	profiles := getActiveProfiles(k)

	// 合并每个profile的配置
	for _, profile := range profiles {
		if profile == "" {
			continue
		}

		// 构建profile配置文件路径
		profileFilePath := filepath.Join(path, utl.JoinString(name, "-", profile, ".", options.configType))

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

// GetKoanf 获取底层koanf实例
func (my *Konfig) GetKoanf() *koanf.Koanf {
	return my.k
}

// Get 获取配置项
func (my *Konfig) Get(path string) interface{} {
	return my.k.Get(path)
}

// Set 设置配置项
func (my *Konfig) Set(path string, value interface{}) {
	my.k.Set(path, value)
}

// IsSet 判断配置项是否存在
func (my *Konfig) IsSet(path string) bool {
	return my.k.Exists(path)
}

// GetString 获取字符串配置
func (my *Konfig) GetString(path string) string {
	return my.k.String(path)
}

// GetBool 获取布尔配置
func (my *Konfig) GetBool(path string) bool {
	return my.k.Bool(path)
}

// GetInt 获取整数配置
func (my *Konfig) GetInt(path string) int {
	return my.k.Int(path)
}

// GetFloat64 获取浮点数配置
func (my *Konfig) GetFloat64(path string) float64 {
	return my.k.Float64(path)
}

// GetDuration 获取时间间隔配置
func (my *Konfig) GetDuration(path string) time.Duration {
	return my.k.Duration(path)
}

// GetStringSlice 获取字符串切片配置
func (my *Konfig) GetStringSlice(path string) []string {
	return my.k.Strings(path)
}

// GetStringMapString 获取字符串映射配置
func (my *Konfig) GetStringMapString(path string) map[string]string {
	return my.k.StringMap(path)
}

// Cut 剪切配置（获取后删除）
func (my *Konfig) Cut(path string) interface{} {
	value := my.Get(path)
	my.k.Delete(path)
	return value
}

// Copy 复制配置
func (my *Konfig) Copy() *Konfig {
	// 创建新的koanf实例
	newKoanf := koanf.New(".")

	// 从原始文件重新加载
	if my.options.filePath != "" {
		_ = newKoanf.Load(file.Provider(my.options.filePath), yaml.Parser())
	}

	return &Konfig{
		k:       newKoanf,
		options: my.options,
	}
}

// Merge 合并配置
func (my *Konfig) Merge(other *Konfig) error {
	// 将other的原始数据转换为map
	otherMap := make(map[string]interface{})
	for key, val := range other.k.Raw() {
		otherMap[key] = val
	}

	// 对每个键进行设置
	for key, val := range otherMap {
		my.k.Set(key, val)
	}

	return nil
}

// Unmarshal 将配置解析到结构体
func (my *Konfig) Unmarshal(val interface{}) error {
	return my.UnmarshalKey("", val)
}

// UnmarshalKey 将配置键解析到结构体
func (my *Konfig) UnmarshalKey(path string, val interface{}) error {
	return my.k.UnmarshalWithConf(path, val, koanf.UnmarshalConf{
		Tag: "mapstructure",
	})
}

func (my *Konfig) UnmarshalWithConf(path string, val interface{}, conf koanf.UnmarshalConf) error {
	return my.k.UnmarshalWithConf(path, val, conf)
}
