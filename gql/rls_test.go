package gql

import (
	"context"
	"database/sql"
	"errors"
	"github.com/ichaly/ideabase/std"
	"net/url"
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
	tx, err := db.Pool().BeginTx(context.Background(), nil)
	require.NoError(t, err)
	err = func() error {
		ctx := context.WithValue(WithIdentity(context.Background(), identity), transactionKey{}, tx)
		reply := executor.run(ctx, `{ users { total } }`, nil, "")
		require.Empty(t, reply.Errors, "%v", reply.Errors)
		var tenant string
		require.NoError(t, tx.QueryRowContext(context.Background(), `SELECT current_setting('gql.tenant', true)`).Scan(&tenant))
		require.Equal(t, "tenant-a", tenant)
		return sentinel
	}()
	require.NoError(t, tx.Rollback())
	require.ErrorIs(t, err, sentinel)
	var tenant string
	require.NoError(t, testQueryRow(db, `SELECT COALESCE(current_setting('gql.tenant', true), '')`).Scan(&tenant))
	require.Empty(t, tenant)
}

func TestRLSActuallyIsolatesRowsForNonBypassRole(t *testing.T) {
	executor, admin, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	testExec(t, admin, `ALTER TABLE users ADD COLUMN tenant_id text NOT NULL DEFAULT 'tenant-a'`)
	testExec(t, admin, `INSERT INTO users(name,email,tenant_id) VALUES
		('A','rls-a@example.com','tenant-a'),('B','rls-b@example.com','tenant-b')`)
	testExec(t, admin, `ALTER TABLE users ENABLE ROW LEVEL SECURITY`)
	testExec(t, admin, `CREATE POLICY gql_tenant_users ON users USING (tenant_id = current_setting('gql.tenant', true))`)
	testExec(t, admin, `CREATE ROLE gql_rls LOGIN PASSWORD 'gql_rls_test' NOSUPERUSER NOBYPASSRLS`)
	testExec(t, admin, `GRANT USAGE ON SCHEMA public TO gql_rls`)
	testExec(t, admin, `GRANT SELECT ON users TO gql_rls`)

	dsnURL, err := url.Parse(admin.DSN())
	require.NoError(t, err)
	dsnURL.User = url.UserPassword("gql_rls", "gql_rls_test")
	dsn := dsnURL.String()
	config := &std.Config{}
	config.Database = dsn
	restricted, err := std.NewConnection(config)
	require.NoError(t, err)
	defer func() {
		_ = restricted.Pool().Close()
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
	testExec(t, db, `ALTER TABLE users ENABLE ROW LEVEL SECURITY`)
	testExec(t, db, `ALTER TABLE users FORCE ROW LEVEL SECURITY`)
	testExec(t, db, `CREATE POLICY gql_test_users ON users USING (true)`)
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
	tx := &sql.Tx{}
	require.False(t, rlsPrepared(ctx, tx))
	prepared := markRLSPrepared(ctx, tx)
	require.True(t, rlsPrepared(prepared, tx))
	require.False(t, rlsPrepared(prepared, &sql.Tx{}))
}
