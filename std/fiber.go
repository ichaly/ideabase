package std

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/compress"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/csrf"
	"github.com/gofiber/fiber/v2/middleware/encryptcookie"
	"github.com/gofiber/fiber/v2/middleware/etag"
	"github.com/gofiber/fiber/v2/middleware/healthcheck"
	"github.com/gofiber/fiber/v2/middleware/idempotency"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/fiber/v2/middleware/requestid"

	"github.com/ichaly/ideabase/utl"
)

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

	// Cookie加密中间件
	if c.EncryptKey != "" {
		// 使用安全填充确保密钥长度为32字节(AES-256)
		paddedKey := utl.SecurePadKey(c.EncryptKey, 32)
		// encryptcookie中间件需要base64编码的密钥
		encodedKey := base64.StdEncoding.EncodeToString([]byte(paddedKey))

		// 配置加密cookie中间件
		app.Use(encryptcookie.New(encryptcookie.Config{
			Key: encodedKey,
		}))
	}

	// 压缩中间件
	app.Use(compress.New(compress.Config{
		Level: compress.LevelDefault,
	}))

	// ETag中间件 - 优化缓存控制
	app.Use(etag.New())

	// 幂等性中间件 - 防止重复处理
	app.Use(idempotency.New(idempotency.Config{
		Lifetime:  24 * time.Hour,
		KeyHeader: "X-Idempotency-Key",
		// 默认情况下会自动跳过安全的HTTP方法（GET、HEAD等）
		// 可以使用Next自定义跳过逻辑
		Next: func(c *fiber.Ctx) bool {
			// 只对这些方法启用幂等性检查
			method := c.Method()
			return !(method == "POST" || method == "PUT" || method == "PATCH" || method == "DELETE")
		},
	}))

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
		Max:        100,             // 最大请求数
		Expiration: 1 * time.Minute, // 时间窗口
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
		LivenessEndpoint:  "/health/live",
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
