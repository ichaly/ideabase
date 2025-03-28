package std

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/knadh/koanf/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKonfigBasic(t *testing.T) {
	// 创建临时配置文件
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	err := os.WriteFile(configPath, []byte("test: value"), 0644)
	require.NoError(t, err)

	// 使用options模式创建配置
	k, err := NewKonfig(WithFilePath(configPath))
	require.NoError(t, err)

	assert.Equal(t, "value", k.GetString("test"))
}

func TestKonfigWatch(t *testing.T) {
	// 创建临时配置文件
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	err := os.WriteFile(configPath, []byte("test: initial"), 0644)
	require.NoError(t, err)

	// 创建通道用于同步测试
	configChanged := make(chan struct{})
	callback := func(k *koanf.Koanf) {
		configChanged <- struct{}{}
	}

	// 使用options模式创建配置
	k, err := NewKonfig(
		WithFilePath(configPath),
		WithDebounceTime(50*time.Millisecond),
		WithConfigChangeCallback(callback),
	)
	require.NoError(t, err)

	// 启动配置监听
	err = k.WatchConfig()
	require.NoError(t, err)

	// 修改配置文件
	err = os.WriteFile(configPath, []byte("test: updated"), 0644)
	require.NoError(t, err)

	// 等待配置变更通知
	select {
	case <-configChanged:
		assert.Equal(t, "updated", k.GetString("test"))
	case <-time.After(time.Second):
		t.Fatal("配置变更回调未在预期时间内执行")
	}

	k.StopWatch()
}

func TestKonfigOptions(t *testing.T) {
	t.Run("默认值测试", func(t *testing.T) {
		k, err := NewKonfig()
		require.NoError(t, err)
		assert.Equal(t, "yaml", k.options.configType)
		assert.Equal(t, "APP", k.options.envPrefix)
		assert.Equal(t, ".", k.options.delim)
		assert.Equal(t, 100*time.Millisecond, k.options.debounceTime)
		assert.Empty(t, k.options.callbacks)
	})

	t.Run("自定义选项测试", func(t *testing.T) {
		callback1 := func(k *koanf.Koanf) {}
		callback2 := func(k *koanf.Koanf) {}

		k, err := NewKonfig(
			WithConfigType("json"),
			WithEnvPrefix("TEST"),
			WithDelimiter("/"),
			WithDebounceTime(200*time.Millisecond),
			WithConfigChangeCallback(callback1),
			WithConfigChangeCallback(callback2),
			WithStrictMerge(true),
		)

		require.NoError(t, err)
		assert.Equal(t, "json", k.options.configType)
		assert.Equal(t, "TEST", k.options.envPrefix)
		assert.Equal(t, "/", k.options.delim)
		assert.Equal(t, 200*time.Millisecond, k.options.debounceTime)
		assert.Len(t, k.options.callbacks, 2)
		assert.True(t, k.options.strict)
	})

	t.Run("配置文件路径测试", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		// 创建测试配置文件
		err := os.WriteFile(configPath, []byte("test: value"), 0644)
		require.NoError(t, err)

		k, err := NewKonfig(WithFilePath(configPath))
		require.NoError(t, err)
		assert.Equal(t, configPath, k.options.filePath)
		assert.Equal(t, "yaml", k.options.configType)
	})

	t.Run("配置复制测试", func(t *testing.T) {
		callback := func(k *koanf.Koanf) {}
		original, err := NewKonfig(
			WithDebounceTime(200*time.Millisecond),
			WithConfigChangeCallback(callback),
		)
		require.NoError(t, err)

		copied := original.Copy()
		assert.Equal(t, original.options.debounceTime, copied.debounceTime)
		assert.Equal(t, len(original.callbacks), len(copied.callbacks))
	})

	t.Run("配置变更回调测试", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		// 创建初始配置文件
		err := os.WriteFile(configPath, []byte("test: initial"), 0644)
		require.NoError(t, err)

		callbackCalled := make(chan struct{})
		callback := func(k *koanf.Koanf) {
			close(callbackCalled)
		}

		k, err := NewKonfig(
			WithFilePath(configPath),
			WithConfigChangeCallback(callback),
			WithDebounceTime(50*time.Millisecond),
		)
		require.NoError(t, err)

		// 启动配置监听
		err = k.WatchConfig()
		require.NoError(t, err)

		// 修改配置文件
		err = os.WriteFile(configPath, []byte("test: updated"), 0644)
		require.NoError(t, err)

		// 等待回调被调用
		select {
		case <-callbackCalled:
			// 回调成功执行
		case <-time.After(time.Second):
			t.Fatal("回调函数未在预期时间内执行")
		}

		k.StopWatch()
	})

	t.Run("防抖功能测试", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		// 创建初始配置文件
		err := os.WriteFile(configPath, []byte("test: initial"), 0644)
		require.NoError(t, err)

		callCount := 0
		callback := func(k *koanf.Koanf) {
			callCount++
		}

		k, err := NewKonfig(
			WithFilePath(configPath),
			WithConfigChangeCallback(callback),
			WithDebounceTime(100*time.Millisecond),
		)
		require.NoError(t, err)

		err = k.WatchConfig()
		require.NoError(t, err)

		// 快速连续修改配置文件
		for i := 0; i < 5; i++ {
			err = os.WriteFile(configPath, []byte(fmt.Sprintf("test: update%d", i)), 0644)
			require.NoError(t, err)
			time.Sleep(20 * time.Millisecond)
		}

		// 等待防抖时间结束
		time.Sleep(200 * time.Millisecond)

		assert.Equal(t, 1, callCount, "防抖功能应该只触发一次回调")

		k.StopWatch()
	})

	t.Run("环境变量测试", func(t *testing.T) {
		os.Setenv("TEST_CONFIG_VALUE", "env_test_value")
		defer os.Unsetenv("TEST_CONFIG_VALUE")

		k, err := NewKonfig(WithEnvPrefix("TEST"))
		require.NoError(t, err)

		assert.Equal(t, "env_test_value", k.GetString("config.value"))
	})

	t.Run("配置合并测试", func(t *testing.T) {
		k1, err := NewKonfig()
		require.NoError(t, err)
		k1.Set("test.key", "value1")

		k2, err := NewKonfig()
		require.NoError(t, err)
		k2.Set("test.key", "value2")

		err = k1.Merge(k2)
		require.NoError(t, err)
		assert.Equal(t, "value2", k1.GetString("test.key"))
	})
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

	// 创建配置变更通道
	configChanged := make(chan *koanf.Koanf, 1)
	callback := func(k *koanf.Koanf) {
		configChanged <- k
	}

	// 创建配置实例，使用新的 options 模式
	cfg, err := NewKonfig(
		WithFilePath(configPath),
		WithDebounceTime(50*time.Millisecond),
		WithConfigChangeCallback(callback),
	)
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

	// 等待配置变更通知，最多等待2秒
	select {
	case newConfig := <-configChanged:
		// 验证回调接收到的配置与实际配置一致
		assert.Equal(t, "UpdatedApp", newConfig.String("app.name"))
		assert.Equal(t, "2.0.0", newConfig.String("app.version"))
	case <-time.After(2 * time.Second):
		t.Fatal("等待配置变更通知超时")
	}

	// 验证配置已被正确更新
	assert.Equal(t, "UpdatedApp", cfg.GetString("app.name"))
	assert.Equal(t, "2.0.0", cfg.GetString("app.version"))
	assert.Equal(t, "db.example.com", cfg.GetString("database.host"))
	assert.Equal(t, 5432, cfg.GetInt("database.port"))
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

	// 创建配置变更通道
	configChanged := make(chan struct{}, 1)
	callback := func(k *koanf.Koanf) {
		configChanged <- struct{}{}
	}

	// 创建配置实例，使用新的 options 模式
	cfg, err := NewKonfig(
		WithFilePath(configPath),
		WithDebounceTime(50*time.Millisecond),
		WithConfigChangeCallback(callback),
	)
	require.NoError(t, err)

	// 验证初始配置（已合并dev环境）
	assert.Equal(t, "DevApp", cfg.GetString("app.name"))
	assert.Equal(t, "localhost", cfg.GetString("database.host"))

	// 启动配置监听
	err = cfg.WatchConfig()
	require.NoError(t, err)
	defer cfg.StopWatch()

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
	select {
	case <-configChanged:
		// 配置变更通知已接收
		assert.Equal(t, "UpdatedDevApp", cfg.GetString("app.name"))
		assert.Equal(t, "localhost", cfg.GetString("database.host"))
	case <-time.After(2 * time.Second):
		t.Fatal("等待配置变更通知超时")
	}
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

	// 创建配置变更通道
	configChanged := make(chan struct{}, 1)
	callback := func(k *koanf.Koanf) {
		configChanged <- struct{}{}
	}

	// 创建配置实例，使用新的 options 模式
	cfg, err := NewKonfig(
		WithFilePath(configPath),
		WithDebounceTime(50*time.Millisecond),
		WithConfigChangeCallback(callback),
	)
	require.NoError(t, err)

	// 启动配置监听
	err = cfg.WatchConfig()
	require.NoError(t, err)
	defer cfg.StopWatch()

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

func TestEnvVarWithWatchConfig(t *testing.T) {
	// 创建临时目录和临时配置文件
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	// 写入初始配置内容
	initialConfig := `
app:
  name: file-name
  version: 1.0.0
env:
  setting: file-setting
`
	err := os.WriteFile(configPath, []byte(initialConfig), 0644)
	require.NoError(t, err)

	// 设置环境变量
	os.Setenv("APP_ENV_SETTING", "env-setting")
	os.Setenv("APP_ENV_ONLY", "env-only-value")
	defer func() {
		os.Unsetenv("APP_ENV_SETTING")
		os.Unsetenv("APP_ENV_ONLY")
		os.Unsetenv("APP_NEW_ENV_VAR")
	}()

	// 创建配置变更通道
	configChanged := make(chan struct{}, 1)
	callback := func(k *koanf.Koanf) {
		configChanged <- struct{}{}
	}

	// 创建配置实例，使用新的 options 模式
	cfg, err := NewKonfig(
		WithFilePath(configPath),
		WithDebounceTime(50*time.Millisecond),
		WithConfigChangeCallback(callback),
	)
	require.NoError(t, err)

	// 验证初始配置（环境变量优先）
	assert.Equal(t, "file-name", cfg.GetString("app.name"))
	assert.Equal(t, "env-setting", cfg.GetString("env.setting")) // 环境变量覆盖文件
	assert.Equal(t, "env-only-value", cfg.GetString("env.only")) // 仅环境变量

	// 启动配置监听
	err = cfg.WatchConfig()
	require.NoError(t, err)
	defer cfg.StopWatch()

	// 修改配置文件
	updatedConfig := `
app:
  name: new-file-name
  version: 2.0.0
env:
  setting: new-file-setting
  file_only: file-only-value
`
	// 确保写入生效
	time.Sleep(50 * time.Millisecond)
	err = os.WriteFile(configPath, []byte(updatedConfig), 0644)
	require.NoError(t, err)

	// 设置新的环境变量
	os.Setenv("APP_NEW_ENV_VAR", "new-env-value")

	// 等待配置变更通知
	select {
	case <-configChanged:
		// 验证更新后的配置
		// 1. 文件中更新的内容
		assert.Equal(t, "new-file-name", cfg.GetString("app.name"))
		assert.Equal(t, "2.0.0", cfg.GetString("app.version"))
		assert.Equal(t, "file-only-value", cfg.GetString("env.file_only"))

		// 2. 环境变量应该仍然覆盖文件
		assert.Equal(t, "env-setting", cfg.GetString("env.setting"))

		// 3. 仅在环境变量中的设置应该保留
		assert.Equal(t, "env-only-value", cfg.GetString("env.only"))

		// 4. 新环境变量应该被正确加载
		assert.Equal(t, "new-env-value", cfg.GetString("new.env.var"))
	case <-time.After(2 * time.Second):
		t.Fatal("等待配置变更通知超时")
	}

	// 确认修改环境变量后WatchConfig不会导致值丢失
	os.Setenv("APP_ENV_SETTING", "updated-env-value")

	// 触发一次配置修改，来确保监听器生效
	minorUpdate := `
app:
  name: minor-update-name
  version: 2.0.0
env:
  setting: new-file-setting
  file_only: file-only-value
`
	err = os.WriteFile(configPath, []byte(minorUpdate), 0644)
	require.NoError(t, err)

	// 等待配置变更通知
	select {
	case <-configChanged:
		// 验证环境变量依然正确覆盖了文件设置
		assert.Equal(t, "minor-update-name", cfg.GetString("app.name"))
		assert.Equal(t, "updated-env-value", cfg.GetString("env.setting"))
	case <-time.After(2 * time.Second):
		t.Fatal("等待配置变更通知超时")
	}
}

func TestKonfigEnvVarSpecifics(t *testing.T) {
	// 清理测试环境变量
	defer func() {
		os.Unsetenv("APP_STRING_VALUE")
		os.Unsetenv("APP_INT_VALUE")
		os.Unsetenv("APP_BOOL_VALUE")
		os.Unsetenv("APP_NESTED_OBJECT_KEY")
		os.Unsetenv("APP_NESTED_ARRAY_0")
		os.Unsetenv("APP_NESTED_ARRAY_1")
		os.Unsetenv("APP_NESTED_ARRAY_2")
		os.Unsetenv("CUSTOM_PREFIX_CUSTOM_KEY")
		os.Unsetenv("APP_PATH_WITH_UNDERSCORES")
		os.Unsetenv("APP_COMPLEX_NESTED_LEVEL1_LEVEL2_VALUE")
	}()

	// 设置各种类型的环境变量
	os.Setenv("APP_STRING_VALUE", "test-string")
	os.Setenv("APP_INT_VALUE", "42")
	os.Setenv("APP_BOOL_VALUE", "true")

	// 嵌套对象
	os.Setenv("APP_NESTED_OBJECT_KEY", "nested-value")

	// 数组
	os.Setenv("APP_NESTED_ARRAY_0", "item1")
	os.Setenv("APP_NESTED_ARRAY_1", "item2")
	os.Setenv("APP_NESTED_ARRAY_2", "item3")

	// 下划线路径转换
	os.Setenv("APP_PATH_WITH_UNDERSCORES", "underscore-value")

	// 深度嵌套
	os.Setenv("APP_COMPLEX_NESTED_LEVEL1_LEVEL2_VALUE", "deeply-nested")

	// 创建默认前缀的Konfig实例
	cfg, err := NewKonfig()
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	// 测试基本类型
	assert.Equal(t, "test-string", cfg.GetString("string.value"))
	assert.Equal(t, 42, cfg.GetInt("int.value"))
	assert.Equal(t, true, cfg.GetBool("bool.value"))

	// 测试嵌套对象
	assert.Equal(t, "nested-value", cfg.GetString("nested.object.key"))

	// 测试数组元素通过索引访问
	assert.Equal(t, "item1", cfg.GetString("nested.array.0"))
	assert.Equal(t, "item2", cfg.GetString("nested.array.1"))
	assert.Equal(t, "item3", cfg.GetString("nested.array.2"))

	// 测试下划线路径
	assert.Equal(t, "underscore-value", cfg.GetString("path.with.underscores"))

	// 测试深度嵌套
	assert.Equal(t, "deeply-nested", cfg.GetString("complex.nested.level1.level2.value"))

	// 测试自定义前缀
	os.Setenv("CUSTOM_PREFIX_CUSTOM_KEY", "custom-value")
	customCfg, err := NewKonfig(WithEnvPrefix("CUSTOM_PREFIX"))
	assert.NoError(t, err)
	assert.Equal(t, "custom-value", customCfg.GetString("custom.key"))

	// 测试自定义分隔符与环境变量
	os.Setenv("APP_DELIM_TEST_KEY", "delim-value")
	delimCfg, err := NewKonfig(WithDelimiter("/"))
	assert.NoError(t, err)
	assert.Equal(t, "delim-value", delimCfg.GetString("delim/test/key"))
}

func TestEnvVarPriority(t *testing.T) {
	// 清理测试环境变量
	defer os.Unsetenv("APP_OVERRIDE_KEY")

	// 创建测试配置文件
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	// 写入配置文件
	baseConfig := `
override:
  key: file-value
unique:
  file: file-only
`
	err := os.WriteFile(configPath, []byte(baseConfig), 0644)
	assert.NoError(t, err)

	// 设置环境变量覆盖
	os.Setenv("APP_OVERRIDE_KEY", "env-value")
	os.Setenv("APP_UNIQUE_ENV", "env-only")

	// 创建Konfig实例
	cfg, err := NewKonfig(WithFilePath(configPath))
	assert.NoError(t, err)

	// 验证环境变量优先级高于文件
	assert.Equal(t, "env-value", cfg.GetString("override.key"))

	// 验证各自独有的键
	assert.Equal(t, "file-only", cfg.GetString("unique.file"))
	assert.Equal(t, "env-only", cfg.GetString("unique.env"))

	// 修改环境变量并重新加载配置
	os.Setenv("APP_OVERRIDE_KEY", "new-env-value")

	// 创建新的Konfig实例加载更新后的环境变量
	newCfg, err := NewKonfig(WithFilePath(configPath))
	assert.NoError(t, err)
	assert.Equal(t, "new-env-value", newCfg.GetString("override.key"))
}

func TestEnvVarWithDotEnv(t *testing.T) {
	// 获取当前工作目录
	currentDir, err := os.Getwd()
	require.NoError(t, err)

	// 保存当前.env文件(如果存在)
	envPath := filepath.Join(filepath.Dir(currentDir), ".env")
	envExists := false
	var originalEnv []byte

	if _, err := os.Stat(envPath); err == nil {
		envExists = true
		originalEnv, err = os.ReadFile(envPath)
		require.NoError(t, err)
	}

	// 创建测试用的.env文件
	testEnv := `
DOTENV_VALUE=from-dotenv
APP_DOTENV_PREFIXED=prefixed-dotenv
`
	err = os.WriteFile(envPath, []byte(testEnv), 0644)
	require.NoError(t, err)

	// 测试完成后恢复原始.env文件
	defer func() {
		if envExists {
			err = os.WriteFile(envPath, originalEnv, 0644)
			assert.NoError(t, err)
		} else {
			err = os.Remove(envPath)
			assert.NoError(t, err)
		}
	}()

	// 设置直接环境变量(优先级更高)
	os.Setenv("APP_OVERRIDE_VALUE", "direct-env")
	defer os.Unsetenv("APP_OVERRIDE_VALUE")

	// 在.env文件中设置相同键但不同值
	err = os.WriteFile(envPath, []byte(testEnv+"\nAPP_OVERRIDE_VALUE=dotenv-version\n"), 0644)
	require.NoError(t, err)

	// 创建Konfig实例
	cfg, err := NewKonfig()
	assert.NoError(t, err)

	// 验证.env加载
	assert.Equal(t, "from-dotenv", os.Getenv("DOTENV_VALUE"))
	assert.Equal(t, "prefixed-dotenv", cfg.GetString("dotenv.prefixed"))

	// 验证直接环境变量优先级高于.env文件
	assert.Equal(t, "direct-env", cfg.GetString("override.value"))
}

func TestEnvVarEdgeCases(t *testing.T) {
	// 清理测试前后的环境变量
	envVars := []string{
		"APP_SPECIAL_CHARS",
		"APP_EMPTY_VALUE",
		"APP_NUMERIC_KEY_123",
		"APP_BOOL_TRUE",
		"APP_BOOL_FALSE",
		"APP_BOOL_INVALID",
		"APP_DURATION_VALUE",
		"APP_FLOAT_VALUE",
		"APP_ENV_WITH_QUOTES",
	}

	// 清理当前可能存在的环境变量
	for _, key := range envVars {
		os.Unsetenv(key)
	}

	// 退出测试时清理测试环境变量
	defer func() {
		for _, key := range envVars {
			os.Unsetenv(key)
		}
	}()

	// 设置测试用的环境变量
	os.Setenv("APP_SPECIAL_CHARS", "value with spaces, commas, and \"quotes\"")
	os.Setenv("APP_EMPTY_VALUE", "")
	os.Setenv("APP_NUMERIC_KEY_123", "numeric-key-value")
	os.Setenv("APP_BOOL_TRUE", "true")
	os.Setenv("APP_BOOL_FALSE", "false")
	os.Setenv("APP_BOOL_INVALID", "not-a-bool")
	os.Setenv("APP_DURATION_VALUE", "15s")
	os.Setenv("APP_FLOAT_VALUE", "3.14159")
	os.Setenv("APP_ENV_WITH_QUOTES", "\"quoted value\"")

	// 创建Konfig实例
	cfg, err := NewKonfig()
	assert.NoError(t, err)

	// 测试特殊字符
	assert.Equal(t, "value with spaces, commas, and \"quotes\"", cfg.GetString("special.chars"))

	// 测试空值
	assert.Equal(t, "", cfg.GetString("empty.value"))
	assert.False(t, cfg.GetBool("empty.value"))
	assert.Equal(t, 0, cfg.GetInt("empty.value"))

	// 测试数值键名
	assert.Equal(t, "numeric-key-value", cfg.GetString("numeric.key.123"))

	// 测试布尔值转换
	assert.True(t, cfg.GetBool("bool.true"))
	assert.False(t, cfg.GetBool("bool.false"))
	assert.False(t, cfg.GetBool("bool.invalid")) // 非有效布尔值应返回false

	// 测试时间间隔转换
	assert.Equal(t, 15*time.Second, cfg.GetDuration("duration.value"))

	// 测试浮点数转换
	assert.InDelta(t, 3.14159, cfg.GetFloat64("float.value"), 0.00001)

	// 测试带引号的值
	assert.Equal(t, "\"quoted value\"", cfg.GetString("env.with.quotes"))

	// 测试读取未设置的环境变量
	assert.Equal(t, "", cfg.GetString("non.existent.key"))
	assert.False(t, cfg.IsSet("non.existent.key"))
}

func TestEnvVarCaseHandling(t *testing.T) {
	// 清理测试环境变量
	defer func() {
		os.Unsetenv("APP_CASE_TEST")
		os.Unsetenv("APP_MIXED_CASE_KEY")
		os.Unsetenv("app_lowercase_key") // 小写前缀
		os.Unsetenv("APP_CAMEL_CASE_KEY")
	}()

	// 设置不同大小写的环境变量
	os.Setenv("APP_CASE_TEST", "uppercase-prefix")
	os.Setenv("APP_MIXED_CASE_KEY", "mixed-case-value")
	os.Setenv("app_lowercase_key", "lowercase-prefix") // 小写前缀通常不会被识别
	os.Setenv("APP_CAMEL_CASE_KEY", "should-become-camelCase")

	// 创建Konfig实例
	cfg, err := NewKonfig()
	assert.NoError(t, err)

	// 测试大写前缀变量
	assert.Equal(t, "uppercase-prefix", cfg.GetString("case.test"))

	// 测试混合大小写键名 (转换为小写)
	assert.Equal(t, "mixed-case-value", cfg.GetString("mixed.case.key"))

	// 测试小写前缀变量 (不应该被加载，因为前缀是大写的APP_)
	assert.False(t, cfg.IsSet("lowercase.key"))
	assert.Equal(t, "", cfg.GetString("lowercase.key"))

	// 路径中的驼峰命名转换
	// 注意：在env.go的callback中，环境变量键名会被转为小写
	assert.Equal(t, "should-become-camelCase", cfg.GetString("camel.case.key"))
}

func TestKonfigDurationParsing(t *testing.T) {
	// 创建临时配置文件，包含duration类型的配置项
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	
	configContent := `
timeout:
  short: 5s
  medium: 1m30s
  long: 2h
  custom: 1h30m45s
intervals:
  heartbeat: 10s
  retry: 500ms
  backoff: 2m
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	// 初始化配置
	k, err := NewKonfig(WithFilePath(configPath))
	require.NoError(t, err)

	// 测试获取duration值
	tests := []struct {
		path     string
		expected time.Duration
	}{
		{"timeout.short", 5 * time.Second},
		{"timeout.medium", 90 * time.Second},
		{"timeout.long", 2 * time.Hour},
		{"timeout.custom", 90*time.Minute + 45*time.Second},
		{"intervals.heartbeat", 10 * time.Second},
		{"intervals.retry", 500 * time.Millisecond},
		{"intervals.backoff", 2 * time.Minute},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			duration := k.GetDuration(tc.path)
			assert.Equal(t, tc.expected, duration, "Duration value for %s should be parsed correctly", tc.path)
		})
	}

	// 测试零值
	zeroPath := "timeout.zero"
	zeroDuration := k.GetDuration(zeroPath)
	assert.Equal(t, time.Duration(0), zeroDuration, "Should return zero duration for undefined path")
}
