package std

import (
	"bytes"
	"errors"
	"maps"
	"strconv"

	"github.com/gofiber/fiber/v3"
)

// Extension GraphQL扩展信息的统一类型
type Extension map[string]interface{}

// Result GraphQL风格的统一响应结构：全站唯一信封，是GraphQL响应的超集
// （data/errors/extensions 与 GraphQL 一致，另加 code/message 供 REST 消费）
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
	cause      error
}

// Location GraphQL错误位置信息
type Location struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

func (my *Exception) Error() string { return my.Message }

func unwrapException(err error) *Exception {
	var ex *Exception
	if errors.As(err, &ex) {
		return ex
	}
	return nil
}

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
	if err == nil {
		return my
	}
	if ex := unwrapException(err); ex != nil {
		ex.cause = ex
		ex.resolveMessage()
		return ex
	}
	if carrier, ok := err.(interface{ Extensions() Extension }); ok {
		if ext := carrier.Extensions(); len(ext) > 0 {
			if my.Extensions == nil {
				my.Extensions = maps.Clone(ext)
			} else {
				maps.Copy(my.Extensions, ext)
			}
		}
	}
	my.cause = err
	my.resolveMessage()
	return my
}

func (my *Exception) WithMessage(message string) *Exception {
	if message != "" {
		my.prompt = message
		my.resolveMessage()
	}
	return my
}

func (my *Exception) resolveMessage() {
	if my.cause != nil {
		my.Message = my.cause.Error()
		return
	}
	if my.prompt != "" {
		my.Message = my.prompt
	}
}

// NewException 创建异常实例
func NewException(statusCode int) *Exception { return &Exception{statusCode: statusCode} }

// wrapJSON 全站唯一信封点：c.JSON 走此编码器，恰好序列化一次。
// 已是 *Result（如 ErrorHandler 自产的成品）原样输出；裸 payload 包一层。
func wrapJSON(v any) ([]byte, error) {
	switch v.(type) {
	case *Result, Result:
		return fiberJSON.Marshal(v)
	}
	return fiberJSON.Marshal(&Result{Code: fiber.StatusOK, Data: v})
}

// envelopeResponse 兜底信封中间件：handler 用 c.Send 直发的 JSON 对象体（如 GraphQL 引擎的
// {data,errors}）在此自动套上 {code,...} 信封，故这类 handler 无需感知 Result。
// c.JSON 响应已是 Result（以 {"code" 起头）故跳过；错误经 c.Next 抛出交 ErrorHandler 定型。
// 注册在中间件最内层，早于 compress/etag 的 body 后处理运行。
func envelopeResponse(c fiber.Ctx) error {
	if err := c.Next(); err != nil {
		return err
	}
	resp := c.Response()
	if !bytes.HasPrefix(resp.Header.ContentType(), []byte(fiber.MIMEApplicationJSON)) {
		return nil
	}
	body := resp.Body()
	if len(body) == 0 || body[0] != '{' || bytes.HasPrefix(body, []byte(`{"code"`)) {
		return nil
	}
	resp.SetBody(envelope(resp.StatusCode(), body))
	return nil
}

// envelope 零解析地把 "code":C 拼进一个 JSON 对象体首部，产出与 Result 同形的
// {"code":C,...}。body 须是 JSON 对象（如 GraphQL 标准体 {data,errors}），避免二次序列化。
func envelope(code int, body []byte) []byte {
	head := strconv.AppendInt([]byte(`{"code":`), int64(code), 10)
	if len(body) <= 2 { // {} 或空体：仅信封
		return append(head, '}')
	}
	return append(append(head, ','), body[1:]...) // {"code":C, + data..}
}

// resultErrorHandler 统一错误出口：任何 handler 返回的 error 在此定型为 Result
func resultErrorHandler(c fiber.Ctx, err error) error {
	status, exceptions := normalizeErrors(err)
	if status <= 0 {
		status = fiber.StatusInternalServerError
	}
	return c.Status(status).JSON(&Result{Code: status, Message: pickMessage(exceptions), Errors: exceptions})
}

func pickMessage(exceptions []*Exception) string {
	for _, ex := range exceptions {
		if ex != nil && ex.Message != "" {
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
