package std

import (
	"encoding/base64"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/extractors"
	"github.com/gofiber/fiber/v3/middleware/compress"
	"github.com/gofiber/fiber/v3/middleware/cors"
	"github.com/gofiber/fiber/v3/middleware/csrf"
	"github.com/gofiber/fiber/v3/middleware/encryptcookie"
	"github.com/gofiber/fiber/v3/middleware/etag"
	"github.com/gofiber/fiber/v3/middleware/idempotency"
	"github.com/gofiber/fiber/v3/middleware/limiter"
	"github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/gofiber/fiber/v3/middleware/requestid"
	"github.com/rs/zerolog"
	"github.com/samber/lo"

	"github.com/ichaly/ideabase/log"
	"github.com/ichaly/ideabase/utl"
)

// NewFiber 创建并配置一个新的fiber应用实例
func NewFiber(c *Config, v *Validator) *fiber.App {
	// 获取Fiber配置（由konfig默认加载）
	fiberConf := c.Fiber

	// 创建fiber应用
	app := fiber.New(fiber.Config{
		AppName:         c.Name,
		ReadTimeout:     fiberConf.ReadTimeout,
		IdleTimeout:     fiberConf.IdleTimeout,
		WriteTimeout:    fiberConf.WriteTimeout,
		ServerHeader:    fiberConf.ServerHeader,
		StructValidator: v,
	})

	// 注册基础中间件
	app.Use(requestid.New()) // 请求ID中间件
	app.Use(recover.New())   // 异常恢复中间件
	app.Use(cors.New())      // 跨域请求支持
	app.Use(etag.New())      // ETag中间件 - 优化缓存控制

	// Cookie加密中间件
	if c.EncryptKey != "" {
		// 使用安全填充确保密钥长度为32字节(AES-256)
		paddedKey := utl.SecurePadKey(c.EncryptKey, 32)
		// encryptcookie中间件需要base64编码的密钥
		encodedKey := base64.StdEncoding.EncodeToString([]byte(paddedKey))

		// 配置加密cookie中间件，排除CSRF cookie
		app.Use(encryptcookie.New(encryptcookie.Config{
			Key:    encodedKey,
			Except: []string{"csrf_"}, // 排除CSRF cookie
		}))
	}

	// CSRF保护中间件 - 防止跨站请求伪造
	if fiberConf.CSRFEnabled {
		extractor := buildCSRFExtractor(fiberConf.CSRFKeyLookup, fiberConf.CSRFCookieName)
		app.Use(csrf.New(csrf.Config{
			CookieName:     fiberConf.CSRFCookieName,
			CookieSameSite: fiberConf.CSRFCookieSameSite,
			IdleTimeout:    fiberConf.CSRFExpiration,
			CookieSecure:   !c.IsDebug(),
			Extractor:      extractor,
			Next: func(c fiber.Ctx) bool {
				path := c.Path()
				// 检查是否匹配跳过的路径前缀
				for _, prefix := range fiberConf.CSRFSkipPrefixes {
					if strings.HasPrefix(path, prefix) {
						return true // 跳过CSRF检查
					}
				}
				return false
			},
		}))
	}

	// 压缩中间件
	app.Use(compress.New(compress.Config{
		Level: compress.Level(fiberConf.CompressLevel),
	}))

	// 幂等性中间件 - 防止重复处理
	app.Use(idempotency.New(idempotency.Config{
		Lifetime:  fiberConf.IdempotencyLifetime,
		KeyHeader: fiberConf.IdempotencyKeyHeader,
	}))

	// 请求限制中间件 - 防止DoS攻击
	app.Use(limiter.New(limiter.Config{
		Max:        fiberConf.LimiterMax, // 最大请求数
		Expiration: fiberConf.LimiterExpiration,
		KeyGenerator: func(c fiber.Ctx) string {
			return c.IP() // 基于IP的限制
		},
		LimitReached: func(c fiber.Ctx) error {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"status":  "error",
				"message": "请求过于频繁，请稍后再试",
			})
		},
	}))

	// 统一响应格式中间件
	skips := lo.FilterMap(fiberConf.ResultSkipRoutes, func(item string, _ int) (string, bool) {
		trimmed := strings.TrimSpace(item)
		return trimmed, trimmed != ""
	})
	options := lo.Ternary(len(skips) > 0, []ResultMiddlewareOption{
		WithResultSkipper(func(route *fiber.Route) bool {
			return route != nil && lo.ContainsBy(skips, func(prefix string) bool {
				return strings.HasPrefix(route.Path, prefix)
			})
		}),
	}, []ResultMiddlewareOption(nil))
	app.Use(ResultMiddleware(options...))

	// 调试模式下添加日志
	if c.IsDebug() {
		app.Use(func(c fiber.Ctx) error {
			start := time.Now()
			err := c.Next()

			status := c.Response().StatusCode()
			logger := log.GetDefault()
			var evt *zerolog.Event
			switch {
			case err != nil:
				evt = logger.Error().Err(err)
			case status >= fiber.StatusBadRequest:
				evt = logger.Warn()
			default:
				evt = logger.Info()
			}

			evt.
				Str("method", c.Method()).
				Str("path", c.Path()).
				Int("status", status).
				Str("ip", c.IP()).
				Dur("latency", time.Since(start)).
				Str("user-agent", c.Get(fiber.HeaderUserAgent)).
				Msg("fiber request")

			return err
		})
	}

	return app
}

func buildCSRFExtractor(expr, cookieName string) extractors.Extractor {
	if expr == "" {
		return extractors.FromHeader(csrf.HeaderName)
	}

	parts := strings.SplitN(expr, ":", 2)
	source := strings.ToLower(strings.TrimSpace(parts[0]))
	key := ""
	if len(parts) > 1 {
		key = strings.TrimSpace(parts[1])
	}

	switch source {
	case "header":
		if key == "" {
			key = csrf.HeaderName
		}
		return extractors.FromHeader(key)
	case "query":
		if key == "" {
			key = "csrf"
		}
		return extractors.FromQuery(key)
	case "param", "params":
		if key == "" {
			key = "csrf"
		}
		return extractors.FromParam(key)
	case "form":
		if key == "" {
			key = "_csrf"
		}
		return extractors.FromForm(key)
	case "cookie":
		if key == "" {
			key = cookieName
			if key == "" {
				key = csrf.ConfigDefault.CookieName
			}
		}
		return extractors.FromCookie(key)
	default:
		// 如果格式不符合预期，则回退到将表达式作为Header名称处理
		return extractors.FromHeader(strings.TrimSpace(expr))
	}
}
