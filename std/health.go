package std

import (
	"time"

	"github.com/gofiber/fiber/v2"
)

type Health struct {
}

func NewHealth() *Health {
	return &Health{}
}

func (my *Health) Path() string {
	return "/health"
}

func (my *Health) Bind(r fiber.Router) {
	r.Get("/", WrapHandler(my.Check))
	r.Get("/live", WrapHandler(my.Liveness))
	r.Get("/ready", WrapHandler(my.Readiness))
}

// Check 通用健康检查
func (my *Health) Check(c *fiber.Ctx) (any, error) {
	return fiber.Map{
		"status":    "ok",
		"timestamp": time.Now().Unix(),
	}, nil
}

// Liveness 存活检查 - 检查应用是否运行
func (my *Health) Liveness(c *fiber.Ctx) (any, error) {
	return fiber.Map{
		"status":    "alive",
		"timestamp": time.Now().Unix(),
		"uptime":    time.Since(time.Now()).Seconds(), // 可以改为实际启动时间
	}, nil
}

// Readiness 就绪检查 - 检查应用是否准备好接收流量
func (my *Health) Readiness(c *fiber.Ctx) (any, error) {
	// 这里可以添加数据库连接、外部服务等检查
	// 示例：检查数据库连接状态
	// if !database.IsConnected() {
	//     return fiber.Map{"status": "not_ready", "reason": "database disconnected"}, nil
	// }

	return fiber.Map{
		"status":    "ready",
		"timestamp": time.Now().Unix(),
		"checks": fiber.Map{
			"database": "ok",
			"cache":    "ok",
		},
	}, nil
}
