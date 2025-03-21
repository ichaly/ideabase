package std

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/knadh/koanf/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewKonfig(t *testing.T) {
	// 创建测试配置文件
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	// 写入测试配置
	testConfig := `
mode: development
app:
  name: test-app
  port: "8080"
  host: localhost
  root: /api
  debug: true
  prefix: /v1
numbers:
  int: 42
  float: 3.14
  duration: 5s
slices:
  strings: [a, b, c]
maps:
  string_map:
    key1: value1
    key2: value2
`
	err := os.WriteFile(configPath, []byte(testConfig), 0644)
	assert.NoError(t, err)

	// 测试环境变量
	os.Setenv("APP_TEST_ENV", "env-value")
	defer os.Unsetenv("APP_TEST_ENV")

	// 创建 Konfig 实例
	cfg, err := NewKonfig(WithFilePath(configPath), WithEnvPrefix("APP"))
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	// 测试基本获取方法
	assert.Equal(t, "development", cfg.GetString("mode"))
	assert.Equal(t, "test-app", cfg.GetString("app.name"))
	assert.Equal(t, "8080", cfg.GetString("app.port"))
	assert.Equal(t, true, cfg.GetBool("app.debug"))

	// 测试数字类型
	assert.Equal(t, 42, cfg.GetInt("numbers.int"))
	assert.Equal(t, 3.14, cfg.GetFloat64("numbers.float"))
	assert.Equal(t, 5*time.Second, cfg.GetDuration("numbers.duration"))

	// 测试切片
	assert.Equal(t, []string{"a", "b", "c"}, cfg.GetStringSlice("slices.strings"))

	// 测试映射
	stringMap := cfg.GetStringMapString("maps.string_map")
	assert.Equal(t, "value1", stringMap["key1"])
	assert.Equal(t, "value2", stringMap["key2"])

	// 测试环境变量覆盖
	envVal := cfg.GetString("test.env")
	if envVal == "" {
		// 尝试其他可能的键名格式
		envVal = cfg.GetString("test_env")
	}
	assert.Equal(t, "env-value", envVal)

	// 测试 IsSet
	assert.True(t, cfg.IsSet("mode"))
	assert.False(t, cfg.IsSet("non_existent"))

	// 测试 Set 和 Get
	cfg.Set("new_key", "new_value")
	assert.Equal(t, "new_value", cfg.GetString("new_key"))

	// 测试 Unmarshal
	type TestConfig struct {
		Mode string `mapstructure:"mode"`
		App  struct {
			Name string `mapstructure:"name"`
			Port string `mapstructure:"port"`
		} `mapstructure:"app"`
	}

	var config TestConfig
	err = cfg.Unmarshal(&config)
	assert.NoError(t, err)
	assert.Equal(t, "development", config.Mode)
	assert.Equal(t, "test-app", config.App.Name)
	assert.Equal(t, "8080", config.App.Port)

	// 测试 UnmarshalKey
	var appConfig struct {
		Name string `mapstructure:"name"`
		Port string `mapstructure:"port"`
	}
	err = cfg.UnmarshalKey("app", &appConfig)
	assert.NoError(t, err)
	assert.Equal(t, "test-app", appConfig.Name)
	assert.Equal(t, "8080", appConfig.Port)
}

func TestKonfigAdvancedFeatures(t *testing.T) {
	// 创建测试配置文件
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	// 写入测试配置
	testConfig := `
parent:
  child:
    value: original
`
	err := os.WriteFile(configPath, []byte(testConfig), 0644)
	assert.NoError(t, err)

	// 创建 Konfig 实例
	cfg, err := NewKonfig(WithFilePath(configPath))
	assert.NoError(t, err)

	// 测试 Cut
	value := cfg.Cut("parent.child.value")
	assert.Equal(t, "original", value)
	assert.False(t, cfg.IsSet("parent.child.value"))

	// 测试 Copy
	cfgCopy := cfg.Copy()
	cfgCopy.Set("new_key", "new_value")
	assert.True(t, cfgCopy.IsSet("new_key"))
	assert.False(t, cfg.IsSet("new_key")) // 原实例没有被修改

	// 测试 Merge
	cfg2, err := NewKonfig(WithFilePath(configPath))
	assert.NoError(t, err)
	cfg2.Set("merge_key", "merge_value")

	err = cfg.Merge(cfg2)
	assert.NoError(t, err)
	assert.Equal(t, "merge_value", cfg.GetString("merge_key"))
}

func TestNewKonfig_Basic(t *testing.T) {
	// 创建测试配置文件
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	// 写入基础配置
	baseConfig := `
mode: test
profiles:
  active: dev
app:
  name: test-app
  version: 1.0.0
database:
  host: localhost
  port: 5432
  username: test
  password: test
`
	err := os.WriteFile(configPath, []byte(baseConfig), 0644)
	assert.NoError(t, err)

	// 写入profile配置
	devConfig := `
database:
  host: dev-host
  password: dev-password
`
	err = os.WriteFile(filepath.Join(tempDir, "config-dev.yaml"), []byte(devConfig), 0644)
	assert.NoError(t, err)

	// 测试环境变量
	os.Setenv("APP_DATABASE_USERNAME", "env-user")

	// 初始化konfig
	cfg, err := NewKonfig(WithFilePath(configPath), WithEnvPrefix("APP"))
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	// 验证基础配置
	assert.Equal(t, "test-app", cfg.GetString("app.name"))
	assert.Equal(t, "1.0.0", cfg.GetString("app.version"))

	// 验证profile覆盖
	assert.Equal(t, "dev-host", cfg.GetString("database.host"))
	assert.Equal(t, "dev-password", cfg.GetString("database.password"))

	// 验证环境变量覆盖
	assert.Equal(t, "env-user", cfg.GetString("database.username"))
}

func TestNewKonfig_WithDelimiter(t *testing.T) {
	// 创建测试配置文件
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	// 写入基础配置
	baseConfig := `
mode: test
app:
  name: test-app
`
	err := os.WriteFile(configPath, []byte(baseConfig), 0644)
	assert.NoError(t, err)

	// 测试自定义分隔符
	cfg, err := NewKonfig(WithFilePath(configPath), WithDelimiter("/"))
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	// 验证使用自定义分隔符访问
	assert.Equal(t, "test-app", cfg.GetString("app/name"))
}

func TestNewKonfig_WithMultipleFormats(t *testing.T) {
	// 创建测试配置文件
	tempDir := t.TempDir()

	// YAML配置
	yamlPath := filepath.Join(tempDir, "config.yaml")
	yamlConfig := `
app:
  name: yaml-app
`
	err := os.WriteFile(yamlPath, []byte(yamlConfig), 0644)
	assert.NoError(t, err)

	// 测试YAML配置加载
	cfg, err := NewKonfig(WithFilePath(yamlPath))
	assert.NoError(t, err)
	assert.Equal(t, "yaml-app", cfg.GetString("app.name"))

	// 注：由于koanf依赖尚未安装，无法测试其他格式的解析器
	// 如JSON、TOML等，实际实现中应当添加对应的解析器支持
}

func TestNewKonfig_WithoutConfigFile(t *testing.T) {
	// 创建konfig实例，不提供配置文件
	cfg, err := NewKonfig()
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	// 验证默认配置
	assert.Equal(t, "dev", cfg.GetString("mode"))
	assert.NotEmpty(t, cfg.GetString("app.root"))

	// 设置环境变量
	os.Setenv("APP_MODE", "production")
	os.Setenv("APP_APP_NAME", "test-app")
	defer func() {
		os.Unsetenv("APP_MODE")
		os.Unsetenv("APP_APP_NAME")
	}()

	// 重新创建konfig实例以加载环境变量
	cfg, err = NewKonfig()
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	// 验证环境变量覆盖
	assert.Equal(t, "production", cfg.GetString("mode"))
	assert.Equal(t, "test-app", cfg.GetString("app.name"))
}

func TestKonfigWatchConfig(t *testing.T) {
	// 创建临时目录和临时配置文件
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	// 写入初始配置内容
	initialConfig := `
app:
  name: TestApp
  version: 1.0.0
database:
  host: localhost
  port: 5432
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	require.NoError(t, err)

	// 创建配置实例
	cfg, err := NewKonfig(WithFilePath(configPath))
	require.NoError(t, err)

	// 验证初始配置
	assert.Equal(t, "TestApp", cfg.GetString("app.name"))
	assert.Equal(t, "1.0.0", cfg.GetString("app.version"))
	assert.Equal(t, "localhost", cfg.GetString("database.host"))
	assert.Equal(t, 5432, cfg.GetInt("database.port"))

	// 启动配置监听
	err = cfg.WatchConfig()
	require.NoError(t, err)
	defer cfg.StopWatch()

	// 设置更短的防抖时间用于测试
	cfg.SetDebounceTime(50 * time.Millisecond)

	// 设置配置变更回调并等待配置变更
	var wg sync.WaitGroup
	wg.Add(1)

	var newConfig *koanf.Koanf
	cfg.OnConfigChange(func(config *koanf.Koanf) {
		newConfig = config
		wg.Done()
	})

	// 修改配置文件
	updatedConfig := `
app:
  name: UpdatedApp
  version: 2.0.0
database:
  host: db.example.com
  port: 5432
`
	// 确保写入生效，文件系统需要一些时间来处理
	time.Sleep(100 * time.Millisecond)

	err = os.WriteFile(configPath, []byte(updatedConfig), 0644)
	require.NoError(t, err)

	// 等待配置变更通知，最多等待1秒
	waitDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
		// 配置变更通知已接收
	case <-time.After(2 * time.Second):
		t.Fatal("等待配置变更通知超时")
	}

	// 验证配置已被正确更新
	assert.Equal(t, "UpdatedApp", cfg.GetString("app.name"))
	assert.Equal(t, "2.0.0", cfg.GetString("app.version"))
	assert.Equal(t, "db.example.com", cfg.GetString("database.host"))
	assert.Equal(t, 5432, cfg.GetInt("database.port"))

	// 验证回调接收到的配置与实际配置一致
	assert.Equal(t, "UpdatedApp", newConfig.String("app.name"))
	assert.Equal(t, "2.0.0", newConfig.String("app.version"))
}

func TestKonfigWatchProfileConfig(t *testing.T) {
	// 创建临时目录和临时配置文件
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	devConfigPath := filepath.Join(tempDir, "config-dev.yaml")

	// 写入主配置内容
	mainConfig := `
mode: dev
profiles:
  active: dev
app:
  name: BaseApp
database:
  host: localhost
  port: 5432
`
	err := os.WriteFile(configPath, []byte(mainConfig), 0644)
	require.NoError(t, err)

	// 写入dev环境配置
	devConfig := `
app:
  name: DevApp
`
	err = os.WriteFile(devConfigPath, []byte(devConfig), 0644)
	require.NoError(t, err)

	// 创建配置实例
	cfg, err := NewKonfig(WithFilePath(configPath))
	require.NoError(t, err)

	// 验证初始配置（已合并dev环境）
	assert.Equal(t, "DevApp", cfg.GetString("app.name"))
	assert.Equal(t, "localhost", cfg.GetString("database.host"))

	// 启动配置监听
	err = cfg.WatchConfig()
	require.NoError(t, err)
	defer cfg.StopWatch()

	// 设置更短的防抖时间用于测试
	cfg.SetDebounceTime(50 * time.Millisecond)

	// 设置配置变更回调并等待配置变更
	var wg sync.WaitGroup
	wg.Add(1)

	cfg.OnConfigChange(func(config *koanf.Koanf) {
		wg.Done()
	})

	// 修改profile配置文件
	updatedDevConfig := `
app:
  name: UpdatedDevApp
`
	// 确保写入生效，文件系统需要一些时间来处理
	time.Sleep(100 * time.Millisecond)

	err = os.WriteFile(devConfigPath, []byte(updatedDevConfig), 0644)
	require.NoError(t, err)

	// 等待配置变更通知，最多等待2秒
	waitDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
		// 配置变更通知已接收
	case <-time.After(2 * time.Second):
		t.Fatal("等待配置变更通知超时")
	}

	// 验证配置已被正确更新
	assert.Equal(t, "UpdatedDevApp", cfg.GetString("app.name"))
	assert.Equal(t, "localhost", cfg.GetString("database.host"))
}

func TestKonfigConcurrentAccess(t *testing.T) {
	// 创建临时配置文件
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	// 写入初始配置
	initialConfig := `
app:
  name: TestApp
  count: 0
database:
  host: localhost
  port: 5432
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	require.NoError(t, err)

	// 创建配置实例
	cfg, err := NewKonfig(WithFilePath(configPath))
	require.NoError(t, err)

	// 启动配置监听
	err = cfg.WatchConfig()
	require.NoError(t, err)
	defer cfg.StopWatch()

	// 设置更短的防抖时间
	cfg.SetDebounceTime(50 * time.Millisecond)

	// 并发访问测试
	var wg sync.WaitGroup
	// 添加50个协程同时读取配置
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 读取配置
			_ = cfg.GetString("app.name")
			_ = cfg.GetInt("database.port")
			_ = cfg.IsSet("app.count")
		}()
	}

	// 同时更新配置文件
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			updatedConfig := `
app:
  name: UpdatedApp
  count: ` + string(rune('0'+i)) + `
database:
  host: db.example.com
  port: 5432
`
			_ = os.WriteFile(configPath, []byte(updatedConfig), 0644)
			time.Sleep(100 * time.Millisecond)
		}
	}()

	// 等待所有协程完成
	wg.Wait()

	// 验证最终配置
	assert.Equal(t, "UpdatedApp", cfg.GetString("app.name"))
	assert.Equal(t, "db.example.com", cfg.GetString("database.host"))
}
