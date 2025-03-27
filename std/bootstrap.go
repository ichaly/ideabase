package std

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/fx"
)

var (
	// Version 当前版本号
	Version = "V0.0.0"
	// GitCommit Git提交哈希
	GitCommit = "Unknown"
	// BuildTime 构建时间
	BuildTime = ""

	// 路由缓存，用于避免重复创建相同基础路径的路由组
	routers = make(map[string]fiber.Router)
	// 路径规范化正则表达式
	reg = regexp.MustCompile(`/+`)
)

// Plugin 插件接口
type Plugin interface {
	// Base 插件基础路径
	Base() string
	// Init 初始化插件
	Init(fiber.Router)
}

// PluginGroup 插件组
type PluginGroup struct {
	fx.In
	Plugins     []Plugin `group:"plugin"`
	Middlewares []Plugin `group:"middleware"`
}

// Bootstrap 应用程序引导函数
func Bootstrap(l fx.Lifecycle, c *Config, a *fiber.App, g PluginGroup) {
	if BuildTime == "" {
		BuildTime = time.Now().Format("2006-01-02 15:04:05")
	}

	// 根路由直接使用app
	routers["/"] = a

	// 获取或创建路由组
	getRouter := func(basePath string) fiber.Router {
		// 规范化路径,将连续的多个斜杠(/)替换为单个斜杠且移除字符串右侧的斜杠
		base := fmt.Sprintf("%s/", strings.TrimRight(reg.ReplaceAllString(basePath, "/"), "/"))

		// 检查缓存
		if r, exists := routers[base]; exists {
			return r
		}

		// 创建新的路由组
		r := a.Group(base)
		routers[base] = r
		return r
	}

	// 注册中间件和插件
	all := append(g.Middlewares, g.Plugins...)
	for _, m := range all {
		router := getRouter(m.Base())
		m.Init(router)
	}

	// 添加生命周期钩子
	l.Append(fx.StartStopHook(func(ctx context.Context) {
		// 异步启动服务器
		go func() {
			addr := fmt.Sprintf(":%v", c.Port)
			if err := a.Listen(addr); err != nil && !errors.Is(err, context.Canceled) {
				fmt.Printf("%v 启动失败: %v\n", c.Name, err)
			}
		}()
	}, func(ctx context.Context) error {
		err := a.Shutdown()
		fmt.Printf("%v 已关闭\n", c.Name)
		return err
	}))

	fmt.Printf("当前版本:%s-%s 发布日期:%s\n", Version, GitCommit, BuildTime)
}
