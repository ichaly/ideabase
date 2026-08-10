package gql

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
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

func TestRLSActuallyIsolatesRowsForNonBypassRole(t *testing.T) {
	executor, admin, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	require.NoError(t, admin.Exec(`ALTER TABLE users ADD COLUMN tenant_id text NOT NULL DEFAULT 'tenant-a'`).Error)
	require.NoError(t, admin.Exec(`INSERT INTO users(name,email,tenant_id) VALUES
		('A','rls-a@example.com','tenant-a'),('B','rls-b@example.com','tenant-b')`).Error)
	require.NoError(t, admin.Exec(`ALTER TABLE users ENABLE ROW LEVEL SECURITY`).Error)
	require.NoError(t, admin.Exec(`CREATE POLICY gql_tenant_users ON users USING (tenant_id = current_setting('gql.tenant', true))`).Error)
	require.NoError(t, admin.Exec(`CREATE ROLE gql_rls LOGIN PASSWORD 'gql_rls_test' NOSUPERUSER NOBYPASSRLS`).Error)
	require.NoError(t, admin.Exec(`GRANT USAGE ON SCHEMA public TO gql_rls`).Error)
	require.NoError(t, admin.Exec(`GRANT SELECT ON users TO gql_rls`).Error)

	dsn := admin.Dialector.(*postgres.Dialector).Config.DSN
	dsn = strings.Replace(strings.Replace(dsn, "user=test", "user=gql_rls", 1), "password=test", "password=gql_rls_test", 1)
	restricted, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	defer func() {
		sqlDB, openErr := restricted.DB()
		if openErr == nil {
			_ = sqlDB.Close()
		}
	}()
	executor.database, executor.rls = restricted, true
	identity, err := NewIdentity(IdentityInput{Tenant: "tenant-a", Subject: "user-a"})
	require.NoError(t, err)
	reply := executor.run(WithIdentity(context.Background(), identity), `{ users(sort: {id: ASC}) { items { email } total } }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	users := reply.Data["users"].(map[string]any)
	require.EqualValues(t, 1, users["total"])
	require.Equal(t, "rls-a@example.com", users["items"].([]any)[0].(map[string]any)["email"])
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

func TestRLSPreparedMarkerIsScopedToTransaction(t *testing.T) {
	ctx := context.Background()
	tx := &gorm.DB{}
	require.False(t, rlsPrepared(ctx, tx))
	prepared := markRLSPrepared(ctx, tx)
	require.True(t, rlsPrepared(prepared, tx))
	require.False(t, rlsPrepared(prepared, &gorm.DB{}))
}
