package gql

import (
	"github.com/gofiber/fiber/v3"
)

// 本文件集中 gql 引擎的 fiber HTTP/WS 适配。引擎只吐 GraphQL 标准体 {data,errors}，
// 对站点信封零感知——套 {code,...} 由 std 的兜底中间件自动完成，错误直接抛给 ErrorHandler。
// WebSocket 依赖 fasthttp（fiber 亦跑其上，无法退回 net/http），故一并用 fiber。

// Bind 实现 Plugin 接口：POST 处理 GraphQL 查询，GET 升级为订阅 WebSocket。
func (my *Engine) Bind(r fiber.Router) {
	r.Post("/", my.Handler)
	r.Get("/", my.SubscribeHandler)
}

// Handler 解析并执行 GraphQL HTTP 请求，直发 GraphQL 标准体 {data,errors}（直通字节零重编码）。
// 边界错误直接 return 交 ErrorHandler 定型为 Result；行级作用域由上游经 gql.WithScope 注入。
func (my *Engine) Handler(c fiber.Ctx) error {
	var req gqlQuery
	if err := jsonNumeric.Unmarshal(c.Request().Body(), &req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	out, err := my.ExecuteOperation(c.Context(), req.Query, req.Variables, req.OperationName)
	if err != nil {
		return err
	}
	// 声明为 GraphQL 响应（GraphQL-over-HTTP 规范媒体类型）：std 兜底中间件据此套 {code,...} 信封
	c.Set(fiber.HeaderContentType, "application/graphql-response+json")
	return c.Send(out)
}

// SubscribeHandler 处理 GraphQL 订阅的 WebSocket 升级（graphql-transport-ws 子协议）。
// 升级前保存完整宿主上下文；升级后请求 ctx 不可用，但认证身份、路由和 scope
// 仍由 UpgradeContext 传入的 context 保持。
func (my *Engine) SubscribeHandler(c fiber.Ctx) error {
	return my.UpgradeContext(c.RequestCtx(), c.Context())
}
