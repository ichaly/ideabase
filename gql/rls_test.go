package gql

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateRLSRoleFailsOpenSecurityProperties(t *testing.T) {
	tables := []RLSTable{{Name: "orders", Protected: true, HasPolicy: true}}
	for _, role := range []RLSRole{{Superuser: true}, {BypassRLS: true}, {OwnerTables: []string{"orders"}}} {
		require.Error(t, ValidateRLS(RLSConfig{Role: role, Tables: tables}))
	}
	require.NoError(t, ValidateRLS(RLSConfig{Role: RLSRole{}, Tables: tables}))
}

func TestValidateRLSRequiresPolicyOrForce(t *testing.T) {
	require.Error(t, ValidateRLS(RLSConfig{Tables: []RLSTable{{Name: "orders", Protected: true}}}))
	require.NoError(t, ValidateRLS(RLSConfig{Tables: []RLSTable{{Name: "orders", Protected: true, Force: true}}}))
	require.NoError(t, ValidateRLS(RLSConfig{Tables: []RLSTable{{Name: "public", Protected: false}}}))
}

func TestRLSIdentityStatementUsesTransactionalParameters(t *testing.T) {
	id, err := NewIdentity(IdentityInput{Tenant: "t' OR true", Subject: "u", Actor: "svc"})
	require.NoError(t, err)
	stmt, err := NewRLSIdentityStatement(id)
	require.NoError(t, err)
	require.Contains(t, stmt.SQL, "set_config")
	require.Contains(t, stmt.SQL, "true")
	require.NotContains(t, stmt.SQL, id.Tenant())
	require.Equal(t, []any{"gql.tenant", "t' OR true"}, stmt.Args[:2])
	require.NotContains(t, stmt.SQL, "SET LOCAL")
}
