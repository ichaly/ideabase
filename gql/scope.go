package gql

import "context"

// 行级作用域（租户/属主隔离）：值经请求上下文注入，不进 schema，对客户端透明。
// 认证中间件解出当前租户/登录人后用 WithScope 注入；实体 scope 配置声明
// 列与上下文键的对应，编译期强制 AND 进 WHERE，执行期从此处取值填参数。

type scopeKey struct{}

// WithScope 把行级作用域值注入请求上下文（认证中间件调用）。
// 键名对应实体 scope 配置的 context；例如：
//
//	ctx = gql.WithScope(ctx, map[string]any{"tenant": claims.Tenant, "userId": claims.UserId})
func WithScope(ctx context.Context, values map[string]any) context.Context {
	return context.WithValue(ctx, scopeKey{}, values)
}

// scopeValues 取出请求上下文中的作用域值表（无则nil，作用域参数解析为nil=匹配不到行）
func scopeValues(ctx context.Context) map[string]any {
	values, _ := ctx.Value(scopeKey{}).(map[string]any)
	return values
}
