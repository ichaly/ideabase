package std

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/fsnotify/fsnotify"
	"github.com/ichaly/ideabase/log"
	"github.com/ichaly/ideabase/utl"
	"github.com/joho/godotenv"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Konfig 配置管理器，包装了koanf.Koanf，并集成了配置文件监听功能
type Konfig struct {
	k            unsafe.Pointer       // 底层koanf实例，使用原子指针操作确保并发安全
	options      *konfigOptions       // 配置选项
	watcher      *fsnotify.Watcher    // 文件监视器
	callbacks    []func(*koanf.Koanf) // 配置变更回调函数列表
	mu           sync.RWMutex         // 互斥锁
	watchActive  int32                // 监听状态，使用原子操作
	stopChan     chan struct{}        // 停止信号通道
	debounceTime time.Duration        // 防抖时间
}

// KonfigOption 定义配置选项函数类型
type KonfigOption func(*konfigOptions)

// konfigOptions 保存koanf的配置选项
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
		options:      options,
		callbacks:    make([]func(*koanf.Koanf), 0),
		debounceTime: 100 * time.Millisecond, // 默认防抖时间
	}
	// 使用原子操作设置koanf指针
	atomic.StorePointer(&konfig.k, unsafe.Pointer(k))

	// 加载环境变量文件(可选)
	if err := loadEnvFile(); err != nil {
		return nil, fmt.Errorf("加载环境变量文件: %w", err)
	}

	// 如果提供了配置文件路径，加载配置
	if options.filePath != "" {
		configK, err := loadKonfigFromFile(options.filePath, options)
		if err != nil {
			return nil, err
		}

		// 合并配置到初始实例
		for key, val := range configK.Raw() {
			k.Set(key, val)
		}
	} else {
		// 即使没有配置文件，也加载环境变量
		envProvider := env.Provider(options.envPrefix+"_", options.delim, func(s string) string {
			return strings.Replace(strings.ToLower(strings.TrimPrefix(s, options.envPrefix+"_")), "_", options.delim, -1)
		})

		if err := k.Load(envProvider, nil); err != nil {
			return nil, fmt.Errorf("加载环境变量失败: %w", err)
		}
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

	// 记录日志
	log.Info().Str("file", filePath).Msg("配置文件已加载")

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
			log.Debug().Str("profile", profile).Str("file", profileFilePath).Msg("配置文件不存在，跳过")
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

		// 记录日志
		log.Info().Str("profile", profile).Str("file", profileFilePath).Msg("配置文件已合并")
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

// loadKoanf 安全获取当前koanf实例
func (my *Konfig) loadKoanf() *koanf.Koanf {
	return (*koanf.Koanf)(atomic.LoadPointer(&my.k))
}

// GetKoanf 获取底层koanf实例
func (my *Konfig) GetKoanf() *koanf.Koanf {
	return my.loadKoanf()
}

// Get 获取配置项
func (my *Konfig) Get(path string) interface{} {
	return my.loadKoanf().Get(path)
}

// Set 设置配置项
func (my *Konfig) Set(path string, value interface{}) {
	my.loadKoanf().Set(path, value)
}

// IsSet 判断配置项是否存在
func (my *Konfig) IsSet(path string) bool {
	return my.loadKoanf().Exists(path)
}

// GetString 获取字符串配置
func (my *Konfig) GetString(path string) string {
	return my.loadKoanf().String(path)
}

// GetBool 获取布尔配置
func (my *Konfig) GetBool(path string) bool {
	return my.loadKoanf().Bool(path)
}

// GetInt 获取整数配置
func (my *Konfig) GetInt(path string) int {
	return my.loadKoanf().Int(path)
}

// GetFloat64 获取浮点数配置
func (my *Konfig) GetFloat64(path string) float64 {
	return my.loadKoanf().Float64(path)
}

// GetDuration 获取时间间隔配置
func (my *Konfig) GetDuration(path string) time.Duration {
	return my.loadKoanf().Duration(path)
}

// GetStringSlice 获取字符串切片配置
func (my *Konfig) GetStringSlice(path string) []string {
	return my.loadKoanf().Strings(path)
}

// GetStringMapString 获取字符串映射配置
func (my *Konfig) GetStringMapString(path string) map[string]string {
	return my.loadKoanf().StringMap(path)
}

// Cut 剪切配置（获取后删除）
func (my *Konfig) Cut(path string) interface{} {
	k := my.loadKoanf()
	value := k.Get(path)
	k.Delete(path)
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

	konfig := &Konfig{
		options:      my.options,
		callbacks:    make([]func(*koanf.Koanf), 0),
		debounceTime: my.debounceTime,
	}
	atomic.StorePointer(&konfig.k, unsafe.Pointer(newKoanf))

	log.Debug().Msg("配置已复制")
	return konfig
}

// Merge 合并配置
func (my *Konfig) Merge(other *Konfig) error {
	// 将other的原始数据转换为map
	k := my.loadKoanf()
	otherK := other.loadKoanf()
	otherMap := make(map[string]interface{})
	for key, val := range otherK.Raw() {
		otherMap[key] = val
	}

	// 对每个键进行设置
	for key, val := range otherMap {
		k.Set(key, val)
	}

	log.Debug().Msg("配置已合并")
	return nil
}

// Unmarshal 将配置解析到结构体
func (my *Konfig) Unmarshal(val interface{}) error {
	return my.UnmarshalKey("", val)
}

// UnmarshalKey 将配置键解析到结构体
func (my *Konfig) UnmarshalKey(path string, val interface{}) error {
	err := my.loadKoanf().UnmarshalWithConf(path, val, koanf.UnmarshalConf{
		Tag: "mapstructure",
	})

	if err != nil {
		log.Error().Err(err).Str("path", path).Msg("配置解析失败")
	}

	return err
}

// UnmarshalWithConf 使用自定义配置解析
func (my *Konfig) UnmarshalWithConf(path string, val interface{}, conf koanf.UnmarshalConf) error {
	err := my.loadKoanf().UnmarshalWithConf(path, val, conf)

	if err != nil {
		log.Error().Err(err).Str("path", path).Msg("配置解析失败")
	}

	return err
}

// SetDefault 设置单个配置项的默认值，与 Viper 风格保持一致
func (my *Konfig) SetDefault(path string, value interface{}) {
	if !my.IsSet(path) {
		my.Set(path, value)
		log.Debug().Str("path", path).Interface("value", value).Msg("设置默认配置项")
	}
}

// SetDefaults 从 map 批量加载默认值
func (my *Konfig) SetDefaults(defaults map[string]interface{}) error {
	// 使用 confmap.Provider 加载默认值
	err := my.loadKoanf().Load(confmap.Provider(defaults, my.options.delim), nil)

	if err != nil {
		log.Error().Err(err).Msg("批量加载默认值失败")
	} else {
		log.Debug().Int("count", len(defaults)).Msg("批量加载默认值成功")
	}

	return err
}

// WatchConfig 启用配置文件变更监听
func (my *Konfig) WatchConfig() error {
	// 使用CAS确保只启动一次
	if !atomic.CompareAndSwapInt32(&my.watchActive, 0, 1) {
		log.Debug().Msg("配置监听已经启动，忽略重复调用")
		return nil // 已经在监听中，直接返回
	}

	// 检查是否有配置文件
	if my.options.filePath == "" {
		atomic.StoreInt32(&my.watchActive, 0)
		return fmt.Errorf("没有设置配置文件路径，无法启动监听")
	}

	// 创建文件监视器
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		atomic.StoreInt32(&my.watchActive, 0)
		return fmt.Errorf("创建文件监视器失败: %w", err)
	}
	my.watcher = watcher
	my.stopChan = make(chan struct{})

	// 监视配置文件所在目录
	dir := filepath.Dir(my.options.filePath)
	if err := my.watcher.Add(dir); err != nil {
		my.StopWatch()
		return fmt.Errorf("添加监视目录失败: %w", err)
	}

	log.Info().Str("directory", dir).Msg("已启动配置文件监听")

	// 启动监视协程
	go my.watchConfigChanges()

	return nil
}

// watchConfigChanges 监视配置文件变更的内部方法
func (my *Konfig) watchConfigChanges() {
	var debounceTimer *time.Timer
	var pendingReload bool

	for {
		select {
		case event, ok := <-my.watcher.Events:
			if !ok {
				return
			}

			// 检查是否是我们关注的配置文件
			if !isTargetConfigFile(event.Name, my.options.filePath) {
				continue
			}

			// 检查是否是写入或创建事件
			if !(event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create) {
				continue
			}

			log.Debug().
				Str("file", event.Name).
				Str("operation", event.Op.String()).
				Msg("检测到配置文件变更")

			pendingReload = true
			// 防抖处理
			if debounceTimer != nil {
				debounceTimer.Stop()
			}

			debounceTimer = time.AfterFunc(my.getDebounceTime(), func() {
				if pendingReload {
					my.reloadConfig()
					pendingReload = false
				}
			})

		case err, ok := <-my.watcher.Errors:
			if !ok {
				return
			}
			// 使用结构化日志记录错误
			log.Error().Err(err).Msg("配置文件监视错误")

		case <-my.stopChan:
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return
		}
	}
}

// 检查目标配置文件，支持多文件匹配
func isTargetConfigFile(eventPath, configPath string) bool {
	baseEventPath := filepath.Base(eventPath)
	baseConfigPath := filepath.Base(configPath)

	// 直接匹配主配置文件
	if baseEventPath == baseConfigPath {
		return true
	}

	// 检查是否匹配profile配置文件
	ext := filepath.Ext(baseConfigPath)
	baseName := baseConfigPath[:len(baseConfigPath)-len(ext)]

	// 匹配 baseName-*.ext 格式的profile配置
	return len(baseEventPath) > len(ext) &&
		strings.HasPrefix(baseEventPath, baseName+"-") &&
		strings.HasSuffix(baseEventPath, ext)
}

// getDebounceTime 安全获取防抖时间
func (my *Konfig) getDebounceTime() time.Duration {
	my.mu.RLock()
	defer my.mu.RUnlock()
	if my.debounceTime <= 0 {
		return 100 * time.Millisecond
	}
	return my.debounceTime
}

// reloadConfig 重新加载配置
func (my *Konfig) reloadConfig() {
	// 使用抽取的方法加载配置
	k, err := loadKonfigFromFile(my.options.filePath, my.options)
	if err != nil {
		log.Error().Err(err).Str("file", my.options.filePath).Msg("重新加载配置失败")
		return
	}

	log.Info().Str("file", my.options.filePath).Msg("配置已重新加载")

	// 原子更新配置指针
	oldK := my.loadKoanf()
	atomic.StorePointer(&my.k, unsafe.Pointer(k))

	// 调用回调函数
	my.notifyCallbacks(k, oldK)
}

// notifyCallbacks 通知所有回调函数
func (my *Konfig) notifyCallbacks(newK, oldK *koanf.Koanf) {
	my.mu.RLock()
	callbacks := make([]func(*koanf.Koanf), len(my.callbacks))
	copy(callbacks, my.callbacks)
	count := len(callbacks)
	my.mu.RUnlock()

	log.Debug().Int("callbackCount", count).Msg("通知配置变更回调")

	for _, callback := range callbacks {
		go callback(newK) // 启动goroutine异步执行回调
	}
}

// StopWatch 停止配置监听
func (my *Konfig) StopWatch() {
	if atomic.CompareAndSwapInt32(&my.watchActive, 1, 0) {
		close(my.stopChan)
		if my.watcher != nil {
			my.watcher.Close()
			my.watcher = nil
			log.Info().Msg("已停止配置文件监听")
		}
	}
}

// OnConfigChange 设置配置变更回调函数
func (my *Konfig) OnConfigChange(callback func(*koanf.Koanf)) {
	my.mu.Lock()
	defer my.mu.Unlock()
	my.callbacks = append(my.callbacks, callback)
	log.Debug().Int("totalCallbacks", len(my.callbacks)).Msg("已添加配置变更回调函数")
}

// SetDebounceTime 设置防抖时间
func (my *Konfig) SetDebounceTime(duration time.Duration) {
	my.mu.Lock()
	defer my.mu.Unlock()
	oldDuration := my.debounceTime
	my.debounceTime = duration
	log.Debug().
		Dur("oldValue", oldDuration).
		Dur("newValue", duration).
		Msg("已设置配置监听防抖时间")
}

// loadKonfigFromFile 从文件加载配置并处理profiles和环境变量
func loadKonfigFromFile(filePath string, options *konfigOptions) (*koanf.Koanf, error) {
	// 创建新的koanf实例
	k := koanf.NewWithConf(koanf.Conf{
		Delim:       options.delim,
		StrictMerge: options.strict,
	})

	// 加载主配置文件
	if err := loadConfigFile(k, filePath, options); err != nil {
		return nil, fmt.Errorf("加载配置文件失败: %w", err)
	}

	// 获取profile路径和名称
	path := filepath.Dir(filePath)
	ext := filepath.Ext(filePath)
	name := strings.TrimSuffix(filepath.Base(filePath), ext)

	// 合并profile配置
	if err := mergeProfiles(k, path, name, options); err != nil {
		return nil, fmt.Errorf("合并环境配置失败: %w", err)
	}

	// 加载环境变量
	envProvider := env.Provider(options.envPrefix+"_", options.delim, func(s string) string {
		return strings.Replace(strings.ToLower(strings.TrimPrefix(s, options.envPrefix+"_")), "_", options.delim, -1)
	})
	if err := k.Load(envProvider, nil); err != nil {
		return nil, fmt.Errorf("加载环境变量失败: %w", err)
	}

	return k, nil
}
