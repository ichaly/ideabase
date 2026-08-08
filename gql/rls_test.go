package gql

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestValidateRLSRoleFailsOpenSecurityProperties(t *testing.T) {
	tables := []RLSTable{{Name: "orders", Protected: true, HasPolicy: true}}
	for _, role := range []RLSRole{{Superuser: true}, {BypassRLS: true}, {OwnerTables: []string{"orders"}}} {
		require.Error(t, ValidateRLS(RLSConfig{Role: role, Tables: tables}))
	}
	require.NoError(t, ValidateRLS(RLSConfig{Role: RLSRole{}, Tables: tables}))
}

func TestRLSExecutionUsesTransactionLocalIdentity(t *testing.T) {
	executor, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	executor.rls = true
	body, err := executor.Execute(context.Background(), `{ users { total } }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(body), "verified identity")

	identity, err := NewIdentity(IdentityInput{Tenant: "tenant-a", Subject: "user-a", Actor: "actor-a"})
	require.NoError(t, err)
	sentinel := errors.New("rollback")
	err = db.Transaction(func(tx *gorm.DB) error {
		ctx := context.WithValue(WithIdentity(context.Background(), identity), transactionKey{}, tx)
		reply := executor.run(ctx, `{ users { total } }`, nil, "")
		require.Empty(t, reply.Errors, "%v", reply.Errors)
		var tenant string
		require.NoError(t, tx.Raw(`SELECT current_setting('gql.tenant', true)`).Scan(&tenant).Error)
		require.Equal(t, "tenant-a", tenant)
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)
	var tenant string
	require.NoError(t, db.Raw(`SELECT COALESCE(current_setting('gql.tenant', true), '')`).Scan(&tenant).Error)
	require.Empty(t, tenant)
}

func TestInspectRLSReadsPostgreSQLCatalog(t *testing.T) {
	executor, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	require.NoError(t, db.Exec(`ALTER TABLE users ENABLE ROW LEVEL SECURITY`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE users FORCE ROW LEVEL SECURITY`).Error)
	require.NoError(t, db.Exec(`CREATE POLICY gql_test_users ON users USING (true)`).Error)
	config, err := InspectRLS(db, executor.metadata)
	require.NoError(t, err)
	found := false
	for _, table := range config.Tables {
		if table.Name == "users" {
			found = true
			require.True(t, table.Protected)
			require.True(t, table.Force)
			require.True(t, table.HasPolicy)
		}
	}
	require.True(t, found)
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
