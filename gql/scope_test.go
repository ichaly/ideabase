package gql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScopeReturnsDefensiveCopy(t *testing.T) {
	values := map[string]any{"tenant": "a", "nested": map[string]any{"region": "cn"}}
	ctx := WithScope(context.Background(), values)
	values["tenant"] = "mutated"
	values["nested"].(map[string]any)["region"] = "mutated"
	got := Scope(ctx)
	require.Equal(t, map[string]any{"tenant": "a", "nested": map[string]any{"region": "cn"}}, got)
	got["tenant"] = "forged"
	got["nested"].(map[string]any)["region"] = "forged"
	require.Equal(t, "a", Scope(ctx)["tenant"])
	require.Equal(t, "cn", Scope(ctx)["nested"].(map[string]any)["region"])
}
