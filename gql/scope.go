package gql

import (
	"context"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/protocol"
)

// 行级作用域（租户/属主隔离）：值经请求上下文注入，不进 schema，对客户端透明。
// 认证中间件解出当前租户/登录人后用 WithScope 注入；实体 scope 配置声明
// 列与上下文键的对应，编译期强制 AND 进 WHERE，执行期从此处取值填参数。

type scopeKey struct{}
type roleKey struct{}
type identityKey struct{}

// WithIdentity attaches one host-verified immutable identity to a request.
func WithIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, identity)
}

func IdentityFromContext(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityKey{}).(Identity)
	return identity, ok
}

// WithRole attaches the authenticated role; roles must not come from GraphQL variables.
func WithRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, roleKey{}, role)
}

// Role returns the authenticated role when present.
func Role(ctx context.Context) (string, bool) {
	if identity, ok := IdentityFromContext(ctx); ok && len(identity.roles) > 0 {
		return identity.roles[0], true
	}
	role, ok := ctx.Value(roleKey{}).(string)
	return role, ok && role != ""
}
func roleValue(ctx context.Context) string { role, _ := Role(ctx); return role }

func Roles(ctx context.Context) []string {
	if identity, ok := IdentityFromContext(ctx); ok {
		return identity.Roles()
	}
	if role, ok := Role(ctx); ok {
		return []string{role}
	}
	return nil
}

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
	identity, ok := IdentityFromContext(ctx)
	if !ok {
		return values
	}
	scope := identity.Claims()
	if scope == nil {
		scope = make(map[string]any, len(values)+4)
	}
	for key, value := range values {
		scope[key] = value
	}
	scope["tenant"] = identity.Tenant()
	scope["subject"] = identity.Subject()
	scope["actor"] = identity.Actor()
	scope["requestId"] = identity.RequestID()
	return scope
}

func scopeWithRole(ctx context.Context, scope map[string]any, needed bool) map[string]any {
	if !needed {
		return scope
	}
	values := make(map[string]any, len(scope)+1)
	for k, v := range scope {
		values[k] = v
	}
	values[protocol.CONTEXT_ROLE] = roleValue(ctx)
	values[protocol.CONTEXT_ROLES] = Roles(ctx)
	return values
}

func scopeWithCursor(ctx context.Context, scope map[string]any) map[string]any {
	values := make(map[string]any, len(scope)+3)
	for key, value := range scope {
		values[key] = value
	}
	tenant, route := "default", "local"
	var revision uint64
	if identity, ok := IdentityFromContext(ctx); ok {
		tenant, revision = identity.Tenant(), identity.PolicyRevision()
	}
	if pinned, ok := PinnedRoute(ctx); ok {
		route = pinned.CacheKey
	}
	values[compiler.CursorTenantContext] = tenant
	values[compiler.CursorRouteContext] = route
	values[compiler.CursorPolicyContext] = revision
	return values
}

func scopeWithAudit(ctx context.Context, scope map[string]any, snapshot string) map[string]any {
	values := make(map[string]any, len(scope)+2)
	for key, value := range scope {
		values[key] = value
	}
	values["snapshot"] = snapshot
	var revision uint64
	if identity, ok := IdentityFromContext(ctx); ok {
		revision = identity.PolicyRevision()
	}
	values["policyRevision"] = revision
	return values
}
