package std

import (
	"errors"
	"fmt"
	"runtime/debug"

	"github.com/gofiber/fiber/v2"
)

// Result GraphQL风格的统一响应结构
type Result struct {
	Data       interface{}  `json:"data,omitempty"`       // 响应数据，成功时存在
	Errors     []*Exception `json:"errors,omitempty"`     // 错误信息，失败时存在
	Extensions Extension    `json:"extensions,omitempty"` // 根级别扩展信息，可选
}

// Extension GraphQL扩展信息的统一类型
type Extension map[string]interface{}

// Exception 统一的异常结构，符合GraphQL错误标准
type Exception struct {
	Message    string        `json:"message"`              // 错误消息（必需）
	Locations  []Location    `json:"locations,omitempty"`  // GraphQL位置信息（可选）
	Path       []interface{} `json:"path,omitempty"`       // 错误路径，支持字符串和数字（可选）
	Extensions Extension     `json:"extensions,omitempty"` // 错误级别扩展信息（可选）

	// 内部字段，不序列化到JSON
	statusCode int // HTTP状态码
}

// Location GraphQL错误位置信息
type Location struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

func (e *Exception) Error() string {
	return e.Message
}

// With 为Exception添加扩展字段，支持链式调用
func (e *Exception) With(key string, value interface{}) *Exception {
	if e.Extensions == nil {
		e.Extensions = make(Extension)
	}
	e.Extensions[key] = value
	return e
}

// NewException 创建错误实例，简洁命名
func NewException(statusCode int, message string, details ...string) *Exception {
	ex := &Exception{
		Message:    message,
		statusCode: statusCode,
	}

	// 设置可选的详情到错误级别的Extensions
	if len(details) > 0 && details[0] != "" {
		ex.Extensions = make(Extension)
		ex.Extensions["details"] = details[0]
	}

	return ex
}

// extensionsKey Context中存储扩展信息的键
const extensionsKey = "response_extensions"

// SetExtension 在Handler中设置响应扩展字段
func SetExtension(c *fiber.Ctx, key string, value interface{}) {
	extensions, _ := c.Locals(extensionsKey).(Extension)
	if extensions == nil {
		extensions = make(Extension)
	}
	extensions[key] = value
	c.Locals(extensionsKey, extensions)
}

// getExtension 从Context获取扩展信息
func getExtension(c *fiber.Ctx) Extension {
	ext, _ := c.Locals(extensionsKey).(Extension)
	return ext
}

// WrapHandler Handler包装器，统一包装响应格式
func WrapHandler(handler func(*fiber.Ctx) (any, error)) fiber.Handler {
	return func(c *fiber.Ctx) (err error) {
		// panic恢复机制
		defer func() {
			if r := recover(); r != nil {
				details := fmt.Sprintf("panic: %v\n%s", r, debug.Stack())
				err = c.Status(fiber.StatusInternalServerError).JSON(Result{
					Errors: []*Exception{NewException(fiber.StatusInternalServerError, "服务器内部错误", details)},
				})
			}
		}()

		// 执行业务逻辑
		data, err := handler(c)
		if err != nil {
			var ex *Exception
			if errors.As(err, &ex) {
				return c.Status(ex.statusCode).JSON(Result{Errors: []*Exception{ex}})
			}
			var fe *fiber.Error
			if errors.As(err, &fe) {
				return c.Status(fe.Code).JSON(Result{Errors: []*Exception{NewException(fe.Code, fe.Message)}})
			}
			return c.Status(fiber.StatusInternalServerError).JSON(Result{
				Errors: []*Exception{NewException(fiber.StatusInternalServerError, "内部服务器错误", err.Error())},
			})
		}

		// 成功响应
		return c.Status(fiber.StatusOK).JSON(Result{Data: data, Extensions: getExtension(c)})
	}
}
