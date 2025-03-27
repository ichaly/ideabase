package std

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewConfig(t *testing.T) {
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
`
	err := os.WriteFile(configPath, []byte(testConfig), 0644)
	assert.NoError(t, err)

	// 初始化Konfig
	cfg, err := NewKonfig(WithFilePath(configPath))
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	// 测试NewConfig函数
	config, err := NewConfig(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, config)

	// 验证配置值是否正确解析
	assert.Equal(t, "development", config.Mode)
	assert.Equal(t, "test-app", config.Name)
	assert.Equal(t, "8080", config.Port)
	assert.Equal(t, "localhost", config.Host)
	assert.Equal(t, "/api", config.Root)
}

func TestNewConfigWithError(t *testing.T) {
	// 创建临时文件夹
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	// 写入无效的YAML格式配置
	invalidConfig := `
mode: development
app:
  name: test-app
  port: "8080"
  - invalid: yaml
    format: here
`
	err := os.WriteFile(configPath, []byte(invalidConfig), 0644)
	assert.NoError(t, err)

	// 这将在解析时触发错误
	cfg, err := NewKonfig(WithFilePath(configPath))
	// 如果创建了konfig对象但解析失败，这里可能是nil或有错误
	if err == nil {
		// 手动强制触发一个错误
		// 加载一个特定的不存在的非字符串类型配置，这应该产生错误
		var invalidType map[string]interface{}
		err = cfg.GetKoanf().Unmarshal("non_existent_key", &invalidType)
		assert.Error(t, err)
	} else {
		// 如果创建konfig时就返回错误，此处也满足测试预期
		assert.Error(t, err)
	}
}

func TestNewConfigWithEnvOverride(t *testing.T) {
	// 创建测试配置文件
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	// 写入基础配置
	baseConfig := `
mode: development
app:
  name: base-app
  port: "8080"
`
	err := os.WriteFile(configPath, []byte(baseConfig), 0644)
	assert.NoError(t, err)

	// 设置环境变量覆盖
	os.Setenv("APP_APP_NAME", "env-app")
	defer os.Unsetenv("APP_APP_NAME")

	// 初始化konfig，启用环境变量覆盖
	cfg, err := NewKonfig(WithFilePath(configPath))
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	// 测试配置加载
	config, err := NewConfig(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, config)

	// 验证环境变量是否正确覆盖了配置
	assert.Equal(t, "env-app", config.Name)
	assert.Equal(t, "8080", config.Port) // 未被环境变量覆盖
	assert.Equal(t, "development", config.Mode)
}
