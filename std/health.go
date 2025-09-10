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
	r.Get("/check", WrapHandler(my.GetCheck))
	r.Post("/check", WrapHandler(my.PostCheck))
}

// GetCheck GET请求不需要CSRF验证
func (my *Health) GetCheck(c *fiber.Ctx) (any, error) {
	return fiber.Map{
		"status":    "ok",
		"timestamp": time.Now().Unix(),
		"method":    "GET",
		"message":   "Health check passed - no CSRF required for GET",
		"client_ip": c.IP(),
	}, nil
}

// PostCheck POST请求需要CSRF验证
func (my *Health) PostCheck(c *fiber.Ctx) (any, error) {
	return fiber.Map{
		"status":    "ok",
		"timestamp": time.Now().Unix(),
		"method":    "POST",
		"message":   "Health check passed - CSRF token validated",
		"client_ip": c.IP(),
	}, nil
}
