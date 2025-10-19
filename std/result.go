package std

import (
	"errors"
	"fmt"
	"maps"
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

func (my *Exception) Error() string {
	return my.Message
}

// With 为Exception添加扩展字段，支持链式调用
func (my *Exception) With(key string, value interface{}) *Exception {
	if value == nil {
		return my
	}
	if my.Extensions == nil {
		my.Extensions = make(Extension)
	}
	my.Extensions[key] = value
	return my
}

// NewException 创建异常实例，仅设置状态码
func NewException(statusCode int) *Exception {
	return &Exception{statusCode: statusCode}
}

// WithMessage 设置错误消息，返回自身便于链式调用
func (my *Exception) WithMessage(message string) *Exception {
	if message != "" {
		my.Message = message
	}
	return my
}

// WithError 绑定底层错误信息，合并扩展并在必要时填充消息
func (my *Exception) WithError(err error) *Exception {
	if carrier, ok := err.(interface{ Extensions() Extension }); ok {
		if ext := carrier.Extensions(); len(ext) > 0 {
			if my.Extensions == nil {
				my.Extensions = maps.Clone(ext)
			} else {
				maps.Copy(my.Extensions, ext)
			}
		}
	}
	if my.Message == "" {
		my.Message = err.Error()
	}
	return my
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
					Errors: []*Exception{
						NewException(fiber.StatusInternalServerError).
							WithMessage("服务器内部错误").
							With("details", details),
					},
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
				return c.Status(fe.Code).JSON(Result{Errors: []*Exception{
					NewException(fe.Code).
						WithMessage(fe.Message).
						WithError(fe),
				}})
			}
			return c.Status(fiber.StatusInternalServerError).JSON(Result{
				Errors: []*Exception{
					NewException(fiber.StatusInternalServerError).
						WithMessage("内部服务器错误").
						WithError(err),
				},
			})
		}
		// 成功响应
		return c.Status(fiber.StatusOK).JSON(Result{Data: data, Extensions: getExtension(c)})
	}
}
