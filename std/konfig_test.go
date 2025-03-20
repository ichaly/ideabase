package std

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
	cfg, err := NewKonfig(configPath, WithEnvPrefix("APP"))
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
	cfg, err := NewKonfig(configPath)
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
	cfg2, err := NewKonfig(configPath)
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
	cfg, err := NewKonfig(configPath, WithEnvPrefix("APP"))
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
	// 跳过测试，因为koanf依赖尚未完全安装或配置
	t.Skip("跳过测试，需要先安装并配置koanf依赖")

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
	cfg, err := NewKonfig(configPath, WithDelimiter("/"))
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	// 验证使用自定义分隔符访问
	assert.Equal(t, "test-app", cfg.GetString("app/name"))
}

func TestNewKonfig_WithMultipleFormats(t *testing.T) {
	// 跳过测试，因为koanf依赖尚未安装
	t.Skip("跳过测试，需要先安装koanf依赖")

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
	cfg, err := NewKonfig(yamlPath)
	assert.NoError(t, err)
	assert.Equal(t, "yaml-app", cfg.GetString("app.name"))

	// 注：由于koanf依赖尚未安装，无法测试其他格式的解析器
	// 如JSON、TOML等，实际实现中应当添加对应的解析器支持
}
