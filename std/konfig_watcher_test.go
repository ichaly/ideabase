package std

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/knadh/koanf/v2"
	"github.com/stretchr/testify/assert"
)

func TestConfigWatcher(t *testing.T) {
	// 创建临时目录和配置文件
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	// 写入初始配置
	initialConfig := `
app:
  name: test-app
  port: "8080"
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	assert.NoError(t, err)

	// 创建初始konfig实例
	cfg, err := NewKonfig(WithFilePath(configPath))
	assert.NoError(t, err)

	// 创建配置监视器
	watcher, err := NewConfigWatcher(cfg.GetKoanf(), configPath)
	assert.NoError(t, err)

	// 设置更短的防抖时间以加快测试
	watcher.SetDebounceTime(100 * time.Millisecond)

	// 用于记录配置变更
	var configChanged bool
	var newKoanf *koanf.Koanf

	// 设置配置变更回调
	watcher.OnChange(func(k *koanf.Koanf) {
		configChanged = true
		newKoanf = k
	})

	// 启动监视器
	err = watcher.Start()
	assert.NoError(t, err)

	// 等待一段时间确保监视器已启动
	time.Sleep(200 * time.Millisecond)

	// 写入新的配置
	newConfig := `
app:
  name: updated-app
  port: "9090"
`
	err = os.WriteFile(configPath, []byte(newConfig), 0644)
	assert.NoError(t, err)

	// 等待配置重新加载
	time.Sleep(300 * time.Millisecond)

	// 验证配置是否已更新
	assert.True(t, configChanged)
	assert.NotNil(t, newKoanf)
	if newKoanf != nil {
		assert.Equal(t, "updated-app", newKoanf.String("app.name"))
		assert.Equal(t, "9090", newKoanf.String("app.port"))
	}

	// 停止监视器
	watcher.Stop()
}

func TestConfigWatcher_MultipleCallbacks(t *testing.T) {
	// 创建临时目录和配置文件
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	// 写入初始配置
	initialConfig := `
app:
  name: test-app
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	assert.NoError(t, err)

	// 创建初始konfig实例
	cfg, err := NewKonfig(WithFilePath(configPath))
	assert.NoError(t, err)

	// 创建配置监视器
	watcher, err := NewConfigWatcher(cfg.GetKoanf(), configPath)
	assert.NoError(t, err)

	// 设置更短的防抖时间
	watcher.SetDebounceTime(100 * time.Millisecond)

	// 用于记录回调次数
	var callback1Called, callback2Called bool

	// 添加多个回调
	watcher.OnChange(func(k *koanf.Koanf) {
		callback1Called = true
	})
	watcher.OnChange(func(k *koanf.Koanf) {
		callback2Called = true
	})

	// 启动监视器
	err = watcher.Start()
	assert.NoError(t, err)

	// 等待监视器启动
	time.Sleep(200 * time.Millisecond)

	// 写入新配置
	newConfig := `
app:
  name: updated-app
`
	err = os.WriteFile(configPath, []byte(newConfig), 0644)
	assert.NoError(t, err)

	// 等待配置重新加载
	time.Sleep(300 * time.Millisecond)

	// 验证所有回调都被调用
	assert.True(t, callback1Called)
	assert.True(t, callback2Called)

	// 停止监视器
	watcher.Stop()
}

func TestConfigWatcher_InvalidConfig(t *testing.T) {
	// 创建临时目录和配置文件
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	// 写入初始配置
	initialConfig := `
app:
  name: test-app
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	assert.NoError(t, err)

	// 创建初始konfig实例
	cfg, err := NewKonfig(WithFilePath(configPath))
	assert.NoError(t, err)

	// 创建配置监视器
	watcher, err := NewConfigWatcher(cfg.GetKoanf(), configPath)
	assert.NoError(t, err)

	// 设置更短的防抖时间
	watcher.SetDebounceTime(100 * time.Millisecond)

	var configChanged bool
	// 添加回调
	watcher.OnChange(func(k *koanf.Koanf) {
		configChanged = true
	})

	// 启动监视器
	err = watcher.Start()
	assert.NoError(t, err)

	// 等待监视器启动
	time.Sleep(200 * time.Millisecond)

	// 写入无效的配置
	invalidConfig := `
app:
  name: test-app
  - invalid: yaml
`
	err = os.WriteFile(configPath, []byte(invalidConfig), 0644)
	assert.NoError(t, err)

	// 等待一段时间
	time.Sleep(300 * time.Millisecond)

	// 验证回调没有被触发（因为配置无效）
	assert.False(t, configChanged)

	// 停止监视器
	watcher.Stop()
}
