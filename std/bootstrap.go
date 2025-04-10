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
	// Version 应用版本号
	Version = "V0.0.0"
	// GitCommit 最后一次Git提交的哈希值
	GitCommit = "Unknown"
	// BuildTime 应用构建时间
	BuildTime = ""

	// 路由缓存，避免重复创建路由组
	routers = make(map[string]fiber.Router)
	// 路径规范化正则，用于处理连续斜杠
	reg = regexp.MustCompile(`/+`)
)

// Plugin 路由插件接口
type Plugin interface {
	// Path 返回插件挂载的基础路径
	Path() string
	// Bind 将插件处理器绑定到路由
	Bind(fiber.Router)
}

// PluginGroup 依赖注入的插件集合
type PluginGroup struct {
	fx.In
	Plugins []Plugin `group:"plugin"` // 功能插件
	Filters []Plugin `group:"filter"` // 过滤器中间件
}

// Bootstrap 应用程序启动引导函数
func Bootstrap(l fx.Lifecycle, c *Config, a *fiber.App, g PluginGroup) {
	if BuildTime == "" {
		BuildTime = time.Now().Format("2006-01-02 15:04:05")
	}

	// 初始化根路由
	routers["/"] = a

	// 获取或创建路由组，避免重复创建
	getRouter := func(basePath string) fiber.Router {
		// 规范化路径：将多个连续斜杠替换为单个，并确保路径以/结尾
		base := fmt.Sprintf("%s/", strings.TrimRight(reg.ReplaceAllString(basePath, "/"), "/"))

		// 优先使用缓存的路由组
		if r, exists := routers[base]; exists {
			return r
		}

		// 创建新路由组并缓存
		r := a.Group(base)
		routers[base] = r
		return r
	}

	// 先注册过滤器再注册插件，确保过滤器先执行
	all := append(g.Filters, g.Plugins...)
	for _, m := range all {
		router := getRouter(m.Path())
		m.Bind(router)
	}

	// 添加应用生命周期钩子：启动与关闭处理
	l.Append(fx.StartStopHook(func(ctx context.Context) {
		// 异步启动HTTP服务器
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
