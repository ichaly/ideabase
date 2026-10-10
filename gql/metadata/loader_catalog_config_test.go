package metadata

import (
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/require"
)

func TestCatalogConfigOverrideMustMatchDatabaseConstraints(t *testing.T) {
	base := &protocol.Class{
		Name: "account", Table: "account", PrimaryKeys: []string{"tenant_id", "code"},
		Fields: map[string]*protocol.Field{
			"tenant_id": {Name: "tenant_id", Column: "tenant_id"},
			"code":      {Name: "code", Column: "code"},
			"email":     {Name: "email", Column: "email"},
		},
		Constraints: []*protocol.Constraint{
			{Name: "account_pk", Kind: protocol.ConstraintPrimary, Columns: []string{"tenant_id", "code"}},
			{Name: "account_email", Kind: protocol.ConstraintUnique, Columns: []string{"email"}},
		},
	}
	loader := NewConfigLoader(&internal.Config{})

	_, err := loader.buildClassFromConfig("account", &internal.ClassConfig{
		Table: "account", PrimaryKeys: []string{"code", "tenant_id"},
	}, base)
	require.ErrorContains(t, err, "catalog主键覆盖冲突")

	_, err = loader.buildClassFromConfig("account", &internal.ClassConfig{
		Table: "account", Fields: map[string]*internal.FieldConfig{"code": {Column: "code", IsUnique: true}},
	}, base)
	require.ErrorContains(t, err, "catalog unique覆盖冲突")

	_, err = loader.buildClassFromConfig("account", &internal.ClassConfig{
		Table: "account", PrimaryKeys: []string{"tenant_id", "code"},
		Fields: map[string]*internal.FieldConfig{"email": {Column: "email", IsUnique: true}},
	}, base)
	require.NoError(t, err)
}
