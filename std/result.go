package std

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"runtime/debug"
	"strings"

	"github.com/gofiber/fiber/v3"

	"github.com/ichaly/ideabase/log"
)

// Extension GraphQL扩展信息的统一类型
type Extension map[string]interface{}

// Result GraphQL风格的统一响应结构
type Result struct {
	Code       int          `json:"code"`
	Data       interface{}  `json:"data,omitempty"`
	Errors     []*Exception `json:"errors,omitempty"`
	Message    string       `json:"message,omitempty"`
	Extensions Extension    `json:"extensions,omitempty"`
}

// Exception 统一的异常结构
type Exception struct {
	Message    string        `json:"message"`
	Locations  []Location    `json:"locations,omitempty"`
	Path       []interface{} `json:"path,omitempty"`
	Extensions Extension     `json:"extensions,omitempty"`

	statusCode int
	prompt     string
	fromError  bool
}

// Location GraphQL错误位置信息
type Location struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

func (my *Exception) Error() string { return my.Message }

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
	if err != nil {
		// 如果调用方已通过 WithMessage 明确指定了对外展示文案，则不要用底层错误覆盖它。
		// 这样既能保留友好提示，也能通过 Extensions 携带字段级校验信息等细节。
		if my.Message == "" {
			my.Message = err.Error()
		}
		my.fromError = true
	}
	return my
}

func (my *Exception) WithMessage(message string) *Exception {
	if message != "" {
		my.prompt = message
		if !my.fromError {
			my.Message = message
		}
	}
	return my
}

// NewException 创建异常实例
func NewException(statusCode int) *Exception { return &Exception{statusCode: statusCode} }

// ResultSkipper 判断是否跳过统一返回
type ResultSkipper func(*fiber.Route) bool

type resultMiddlewareConfig struct {
	skipper ResultSkipper
}

// ResultMiddlewareOption 中间件配置项
type ResultMiddlewareOption func(*resultMiddlewareConfig)

// WithResultSkipper 指定跳过统一返回的路由
func WithResultSkipper(skipper ResultSkipper) ResultMiddlewareOption {
	return func(cfg *resultMiddlewareConfig) {
		cfg.skipper = skipper
	}
}

// ResultMiddleware 零侵入统一返回中间件
func ResultMiddleware(options ...ResultMiddlewareOption) fiber.Handler {
	cfg := resultMiddlewareConfig{}
	for _, opt := range options {
		opt(&cfg)
	}

	return func(c fiber.Ctx) (err error) {
		if shouldSkip(cfg.skipper, c.Route()) {
			return c.Next()
		}

		defer func() {
			if r := recover(); r != nil {
				err = respondPanic(c, r)
			}
		}()

		if err = c.Next(); err != nil {
			return respondError(c, err)
		}

		if shouldSkip(cfg.skipper, c.Route()) {
			return nil
		}

		status := c.Response().StatusCode()
		if status == 0 {
			status = fiber.StatusOK
		}
		if status >= fiber.StatusBadRequest {
			return nil
		}

		body := c.Response().Body()
		if len(body) == 0 || !isJSONResponse(c) {
			return nil
		}

		data, wrapped := parsePayload(body)
		if wrapped || data == nil {
			return nil
		}

		c.Response().ResetBody()
		return respondSuccess(c, status, data)
	}
}

func respondError(c fiber.Ctx, err error) error {
	status, exceptions := normalizeErrors(err)
	return writeErrors(c, status, exceptions...)
}

func respondPanic(c fiber.Ctx, r interface{}) error {
	stack := debug.Stack()
	log.GetDefault().
		Error().
		Str("panic", fmt.Sprintf("%v", r)).
		Bytes("stack", stack).
		Msg("fiber panic recovered")

	exception := NewException(fiber.StatusInternalServerError).WithMessage("服务器内部错误")
	return writeErrors(c, fiber.StatusInternalServerError, exception)
}

func respondSuccess(c fiber.Ctx, status int, data interface{}) error {
	if status <= 0 {
		status = fiber.StatusOK
	}
	return c.Status(status).JSON(Result{Code: status, Message: "", Data: data})
}

func writeErrors(c fiber.Ctx, status int, exceptions ...*Exception) error {
	if status <= 0 {
		status = fiber.StatusInternalServerError
	}
	msg := pickMessage(exceptions)
	return c.Status(status).JSON(Result{Code: status, Message: msg, Errors: exceptions})
}

func pickMessage(exceptions []*Exception) string {
	for _, ex := range exceptions {
		if ex == nil {
			continue
		}
		if ex.prompt != "" {
			return ex.prompt
		}
		if ex.Message != "" {
			return ex.Message
		}
	}
	return ""
}

func normalizeErrors(err error) (int, []*Exception) {
	var exception *Exception
	if errors.As(err, &exception) {
		return normalizeStatus(exception.statusCode, fiber.StatusInternalServerError), []*Exception{exception}
	}

	var fe *fiber.Error
	if errors.As(err, &fe) {
		return fe.Code, []*Exception{NewException(fe.Code).WithMessage(fe.Message).WithError(err)}
	}

	if _, ok := err.(interface{ Extensions() Extension }); ok {
		exception = NewException(fiber.StatusBadRequest).WithError(err)
		return fiber.StatusBadRequest, []*Exception{exception}
	}

	exception = NewException(fiber.StatusInternalServerError).WithError(err)
	return fiber.StatusInternalServerError, []*Exception{exception}
}

func normalizeStatus(status, fallback int) int {
	if status <= 0 {
		return fallback
	}
	return status
}

func shouldSkip(skipper ResultSkipper, route *fiber.Route) bool {
	if skipper == nil || route == nil {
		return false
	}
	return skipper(route)
}

func isJSONResponse(c fiber.Ctx) bool {
	contentType := strings.ToLower(string(c.Response().Header.ContentType()))
	if contentType == "" {
		return true
	}
	return strings.Contains(contentType, fiber.MIMEApplicationJSON)
}

func parsePayload(body []byte) (interface{}, bool) {
	var payload interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false
	}

	obj, ok := payload.(map[string]interface{})
	if !ok {
		return payload, false
	}

	wrapped, hasCode := true, false
	for key := range obj {
		switch key {
		case "code":
			hasCode = true
		case "message", "data", "errors", "extensions":
		default:
			wrapped = false
			break
		}
	}

	if wrapped && hasCode {
		return nil, true
	}
	return obj, false
}
