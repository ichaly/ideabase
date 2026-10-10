package protocol

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpsertConflictTargetsMapsGraphQLScopeFieldsToColumns(t *testing.T) {
	class := &Class{
		PrimaryKeys: []string{"tenantId", "code"},
		Fields: map[string]*Field{
			"tenantId": {Name: "tenantId", Column: "tenant_id", IsPrimary: true},
			"code":     {Name: "code", Column: "code", IsPrimary: true},
		},
		Scope: []ScopeRule{{Column: "tenant_id", Context: "tenant"}},
	}

	keys := class.UpsertConflictTargets()
	require.Len(t, keys, 1)
	require.True(t, keys[0].Primary)
}
