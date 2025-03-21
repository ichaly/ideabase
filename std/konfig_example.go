//go:build example
// +build example

package std

import (
	"fmt"
	"log"
	"time"

	"github.com/knadh/koanf/v2"
)

// KonfigExample 展示如何使用konfig配置工具类
func KonfigExample() {
	// 创建konfig配置实例
	// 参数1: 配置文件路径
	// 参数2..n: 配置选项
	cfg, err := NewKonfig(
		WithFilePath("config.yaml"),
		WithEnvPrefix("APP"),
		WithConfigType("yaml"),
		WithDelimiter("."),
		WithStrictMerge(true),
	)
	if err != nil {
		log.Fatalf("创建konfig配置实例失败: %v", err)
	}

	// 读取配置项
	appName := cfg.GetString("app.name")         // 获取字符串类型配置
	debug := cfg.GetBool("app.debug")            // 获取布尔类型配置
	port := cfg.GetInt("server.port")            // 获取整数类型配置
	timeout := cfg.GetDuration("server.timeout") // 获取时间间隔类型配置

	fmt.Printf("应用名称: %s\n", appName)
	fmt.Printf("调试模式: %v\n", debug)
	fmt.Printf("服务端口: %d\n", port)
	fmt.Printf("超时时间: %s\n", timeout)

	// 获取嵌套结构
	dbConfig := cfg.Cut("database") // 获取database开头的所有配置项
	fmt.Printf("数据库主机: %s\n", dbConfig.(map[string]interface{})["host"])
	fmt.Printf("数据库端口: %d\n", int(dbConfig.(map[string]interface{})["port"].(float64)))

	// 启动配置文件监听
	if err := cfg.WatchConfig(); err != nil {
		log.Fatalf("启动配置文件监听失败: %v", err)
	}
	defer cfg.StopWatch() // 确保程序结束时停止监听

	// 设置防抖时间
	cfg.SetDebounceTime(200 * time.Millisecond)

	// 设置配置文件变更回调函数
	cfg.OnConfigChange(func(newConfig *koanf.Koanf) {
		fmt.Println("配置文件已更新!")
		fmt.Printf("新的应用名称: %s\n", newConfig.String("app.name"))

		// 配置变更后可以在这里执行一些操作
		// 比如重新初始化数据库连接、更新日志级别等
	})

	// 应用程序运行中...
	fmt.Println("应用程序运行中，请修改config.yaml文件以测试配置热重载...")
	time.Sleep(time.Minute) // 模拟应用程序运行
}

/*
示例配置文件 config.yaml:

app:
  name: IdeaBase
  debug: true
  version: 1.0.0

server:
  port: 8080
  host: 0.0.0.0
  timeout: 30s

database:
  host: localhost
  port: 5432
  username: postgres
  password: postgres
  database: ideabase
  pool:
    max_open: 10
    max_idle: 5
    timeout: 5s

log:
  level: info
  format: json
  output: console

profiles:
  active: dev
*/
