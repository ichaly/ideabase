package gql

import (
	"context"
	"testing"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/require"
)

func TestWithRoleAndRoleScope(t *testing.T) {
	ctx := WithRole(context.Background(), "editor")
	if got := roleValue(ctx); got != "editor" {
		t.Fatalf("role=%q", got)
	}
	scope := scopeWithRole(ctx, map[string]any{"tenant": 1}, true)
	if scope[protocol.CONTEXT_ROLE] != "editor" {
		t.Fatalf("scope role=%v", scope[protocol.CONTEXT_ROLE])
	}
}

func TestIdentityNormalizesTrustedScopeAndLegacyRole(t *testing.T) {
	identity, err := NewIdentity(IdentityInput{
		Tenant: "tenant-1", Subject: "user-1", Actor: "service-1", RequestID: "request-1",
		Roles: []string{"reader", "auditor"}, Claims: map[string]any{"region": "cn", "tenant": "spoof"},
	})
	require.NoError(t, err)
	ctx := WithIdentity(context.Background(), identity)
	got, ok := IdentityFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "tenant-1", got.Tenant())
	require.Equal(t, "reader", roleValue(ctx), "旧单角色入口使用首个已验证角色")
	require.Equal(t, []string{"reader", "auditor"}, Roles(ctx))

	scope := scopeValues(ctx)
	require.Equal(t, "tenant-1", scope["tenant"], "受控身份字段覆盖同名claim")
	require.Equal(t, "user-1", scope["subject"])
	require.Equal(t, "service-1", scope["actor"])
	require.Equal(t, "request-1", scope["requestId"])
	require.Equal(t, "cn", scope["region"])
}
