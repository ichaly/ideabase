package std

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/compress"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/csrf"
	"github.com/gofiber/fiber/v2/middleware/etag"
	"github.com/gofiber/fiber/v2/middleware/healthcheck"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/fiber/v2/middleware/requestid"
	"github.com/google/uuid"
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

// IdempotencyKey 表示一个幂等性键，用于标识请求的唯一性
type IdempotencyKey struct {
	// 键值
	Value string
	// 过期时间
	ExpireAt time.Time
	// 处理结果
	Result []byte
	// 状态码
	StatusCode int
	// 响应头
	Headers map[string]string
}

// IdempotencyStore 幂等性键值存储接口
type IdempotencyStore interface {
	// Get 获取幂等性键对应的值
	Get(key string) (*IdempotencyKey, bool)
	// Set 设置幂等性键值对
	Set(key string, value *IdempotencyKey)
	// Delete 删除幂等性键值对
	Delete(key string)
}

// InMemoryIdempotencyStore 基于内存的幂等性键值存储实现
type InMemoryIdempotencyStore struct {
	mu    sync.RWMutex
	store map[string]*IdempotencyKey
}

// NewInMemoryIdempotencyStore 创建一个新的内存幂等性存储
func NewInMemoryIdempotencyStore() *InMemoryIdempotencyStore {
	store := &InMemoryIdempotencyStore{
		store: make(map[string]*IdempotencyKey),
	}
	
	// 启动一个后台协程清理过期的键
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			store.cleanup()
		}
	}()
	
	return store
}

// Get 获取幂等性键对应的值
func (my *InMemoryIdempotencyStore) Get(key string) (*IdempotencyKey, bool) {
	my.mu.RLock()
	defer my.mu.RUnlock()
	
	value, exists := my.store[key]
	if !exists {
		return nil, false
	}
	
	// 检查键是否过期
	if time.Now().After(value.ExpireAt) {
		return nil, false
	}
	
	return value, true
}

// Set 设置幂等性键值对
func (my *InMemoryIdempotencyStore) Set(key string, value *IdempotencyKey) {
	my.mu.Lock()
	defer my.mu.Unlock()
	
	my.store[key] = value
}

// Delete 删除幂等性键值对
func (my *InMemoryIdempotencyStore) Delete(key string) {
	my.mu.Lock()
	defer my.mu.Unlock()
	
	delete(my.store, key)
}

// cleanup 清理过期的键
func (my *InMemoryIdempotencyStore) cleanup() {
	my.mu.Lock()
	defer my.mu.Unlock()
	
	now := time.Now()
	for key, value := range my.store {
		if now.After(value.ExpireAt) {
			delete(my.store, key)
		}
	}
}

// IdempotencyConfig 幂等性中间件配置
type IdempotencyConfig struct {
	// 启用幂等性保护
	Enabled bool
	// 用于提取幂等性键的请求头名称
	KeyHeader string
	// 幂等性键存储
	Store IdempotencyStore
	// 键过期时间
	KeyExpiration time.Duration
	// 只对指定的HTTP方法启用幂等性保护
	Methods []string
}

// DefaultIdempotencyConfig 默认幂等性配置
func DefaultIdempotencyConfig() IdempotencyConfig {
	return IdempotencyConfig{
		Enabled:       true,
		KeyHeader:     "X-Idempotency-Key",
		Store:         NewInMemoryIdempotencyStore(),
		KeyExpiration: 24 * time.Hour,
		Methods:       []string{"POST", "PUT", "PATCH", "DELETE"},
	}
}

// IdempotencyMiddleware 创建一个幂等性中间件
func IdempotencyMiddleware(config IdempotencyConfig) fiber.Handler {
	// 创建方法映射用于快速查找
	methodMap := make(map[string]bool)
	for _, method := range config.Methods {
		methodMap[method] = true
	}
	
	return func(c *fiber.Ctx) error {
		// 如果中间件被禁用或请求方法不需要幂等性保护，直接继续处理
		if !config.Enabled || !methodMap[c.Method()] {
			return c.Next()
		}
		
		// 从请求头获取幂等性键
		idempotencyKey := c.Get(config.KeyHeader)
		if idempotencyKey == "" {
			// 如果没有提供幂等性键，生成一个新的并添加到响应头
			idempotencyKey = uuid.New().String()
			c.Set(config.KeyHeader, idempotencyKey)
			return c.Next()
		}
		
		// 检查是否已存在处理结果
		if existingKey, found := config.Store.Get(idempotencyKey); found {
			// 返回之前的处理结果
			for name, value := range existingKey.Headers {
				c.Set(name, value)
			}
			c.Set("X-Idempotent-Replayed", "true")
			return c.Status(existingKey.StatusCode).Send(existingKey.Result)
		}
		
		// 设置捕获标志
		c.Set("X-Idempotent-Original", "true")
		
		// 继续处理请求
		err := c.Next()
		if err != nil {
			return err
		}
		
		// 获取处理结果
		responseBody := c.Response().Body()
		statusCode := c.Response().StatusCode()
		
		// 捕获头信息
		headers := make(map[string]string)
		c.Response().Header.VisitAll(func(key, value []byte) {
			headers[string(key)] = string(value)
		})
		
		// 存储处理结果
		config.Store.Set(idempotencyKey, &IdempotencyKey{
			Value:      idempotencyKey,
			ExpireAt:   time.Now().Add(config.KeyExpiration),
			Result:     responseBody,
			StatusCode: statusCode,
			Headers:    headers,
		})
		
		return nil
	}
}

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

// NewFiber 创建并配置一个新的fiber应用实例
func NewFiber(c *Config) *fiber.App {
	// 创建fiber应用配置
	config := fiber.Config{
		AppName:      c.Name,
		ServerHeader: "IdeaBase",
		// 添加超时处理
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// 生产模式下调整配置
	if !c.IsDebug() {
		config.DisableStartupMessage = true
	}

	// 创建fiber应用
	app := fiber.New(config)

	// 注册基础中间件
	app.Use(recover.New())   // 异常恢复中间件
	app.Use(cors.New())      // 跨域请求支持
	app.Use(requestid.New()) // 请求ID中间件

	// 压缩中间件
	app.Use(compress.New(compress.Config{
		Level: compress.LevelDefault,
	}))

	// ETag中间件 - 优化缓存控制
	app.Use(etag.New())

	// 幂等性中间件 - 防止重复处理
	app.Use(IdempotencyMiddleware(DefaultIdempotencyConfig()))

	// CSRF保护中间件 - 防止跨站请求伪造
	app.Use(csrf.New(csrf.Config{
		KeyLookup:      "header:X-CSRF-Token",
		CookieName:     "csrf_",
		CookieSameSite: "Strict",
		Expiration:     1 * time.Hour,
		// 调试模式下可以关闭
		CookieSecure: !c.IsDebug(),
	}))

	// 请求限制中间件 - 防止DoS攻击
	app.Use(limiter.New(limiter.Config{
		Max:        100,              // 最大请求数
		Expiration: 1 * time.Minute,  // 时间窗口
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP() // 基于IP的限制
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"status":  "error",
				"message": "请求过于频繁，请稍后再试",
			})
		},
	}))

	// 健康检查中间件
	app.Use(healthcheck.New(healthcheck.Config{
		LivenessEndpoint: "/health/live",
		ReadinessEndpoint: "/health/ready",
	}))

	// 超时处理 - 设置为全局超时
	app.Use(func(c *fiber.Ctx) error {
		// 设置超时上下文
		ctx, cancel := context.WithTimeout(c.Context(), 30*time.Second)
		defer cancel()
		
		// 替换请求上下文
		c.SetUserContext(ctx)
		
		// 继续处理请求
		err := c.Next()
		
		// 检查是否超时
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return c.Status(fiber.StatusRequestTimeout).JSON(fiber.Map{
				"status":  "error",
				"message": "请求处理超时",
			})
		}
		
		return err
	})

	// 调试模式下添加日志
	if c.IsDebug() {
		app.Use(logger.New(logger.Config{
			Format: "[${time}] ${ip} ${status} - ${method} ${path}\n",
		}))
	}

	return app
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
