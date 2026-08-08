package gql

import (
	"context"
	"github.com/ichaly/ideabase/gql/protocol"
	"testing"
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
