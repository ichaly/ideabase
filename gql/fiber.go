package gql

import (
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v3"
)

// 本文件集中 gql 引擎的 fiber HTTP/WS 适配。引擎只吐 GraphQL 标准体 {data,errors}，
// 对站点信封零感知——套 {code,...} 由 std 的兜底中间件自动完成，错误直接抛给 ErrorHandler。
// WebSocket 依赖 fasthttp（fiber 亦跑其上，无法退回 net/http），故一并用 fiber。

// Bind 实现 Plugin 接口：POST 处理 GraphQL 查询，GET 升级为订阅 WebSocket。
func (my *Executor) Bind(r fiber.Router) {
	r.Post("/", my.Handler)
	r.Get("/", my.SubscribeHandler)
}

// Handler 解析并执行 GraphQL HTTP 请求，直发 GraphQL 标准体 {data,errors}（直通字节零重编码）。
// 边界错误直接 return 交 ErrorHandler 定型为 Result；行级作用域由上游经 gql.WithScope 注入。
func (my *Executor) Handler(c fiber.Ctx) error {
	var req gqlQuery
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	// persisted-only：HTTP边界只接受持久化操作，原始查询文本一律拒绝
	if my.options().PersistedOnly && strings.TrimSpace(req.Query) != "" {
		return fiber.NewError(fiber.StatusForbidden, "仅接受持久化操作（省略query，以operationName执行已注册文档）")
	}

	// 空查询且携带操作名时按持久化查询执行
	if strings.TrimSpace(req.Query) == "" && req.OperationName != "" {
		query, ok := my.documents[req.OperationName]
		if !ok {
			return fiber.NewError(fiber.StatusBadRequest, fmt.Sprintf("未找到名为'%s'的持久化操作", req.OperationName))
		}
		req.Query = query
	}

	out, err := my.executeBytes(c.Context(), req.Query, req.Variables, req.OperationName)
	if err != nil {
		return err
	}
	// 声明为 GraphQL 响应（GraphQL-over-HTTP 规范媒体类型）：std 兜底中间件据此套 {code,...} 信封
	c.Set(fiber.HeaderContentType, "application/graphql-response+json")
	return c.Send(out)
}

// SubscribeHandler 处理 GraphQL 订阅的 WebSocket 升级（graphql-transport-ws 子协议）。
// 升级前提取行级作用域并透传（升级后请求 ctx 不可用，否则订阅丢失隔离）。
func (my *Executor) SubscribeHandler(c fiber.Ctx) error {
	return my.Upgrade(c.RequestCtx(), scopeValues(c.Context()))
}
