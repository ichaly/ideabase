package gql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPostgresSecurityStateIsSharedAcrossInstances(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	require.NoError(t, db.Exec(PostgresSecurityStateDDL).Error)
	one, err := NewPostgresSecurityStateProvider(db)
	require.NoError(t, err)
	two, err := NewPostgresSecurityStateProvider(db)
	require.NoError(t, err)
	identity, err := NewIdentity(IdentityInput{Tenant: "acme", Subject: "user", PolicyRevision: 2, CredentialEpoch: 3})
	require.NoError(t, err)
	route := TenantRoute{Tenant: "acme", Revision: 4}

	require.NoError(t, one.Replace(context.Background(), 1, map[string]TenantSecurityState{
		"acme": {MinimumPolicyRevision: 2, MinimumCredentialEpoch: 3, MinimumRouteRevision: 4},
	}))
	require.NoError(t, two.Check(context.Background(), identity, route))
	require.NoError(t, one.Replace(context.Background(), 2, map[string]TenantSecurityState{
		"acme": {Disabled: true},
	}))
	require.ErrorContains(t, two.Check(context.Background(), identity, route), "disabled")
}

func TestPostgresSecurityStateRejectsStaleAndConflictingRevision(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	require.NoError(t, db.Exec(PostgresSecurityStateDDL).Error)
	provider, err := NewPostgresSecurityStateProvider(db)
	require.NoError(t, err)
	require.NoError(t, provider.Replace(context.Background(), 2, map[string]TenantSecurityState{"acme": {}}))
	require.NoError(t, provider.Replace(context.Background(), 2, map[string]TenantSecurityState{"acme": {}}))
	require.ErrorContains(t, provider.Replace(context.Background(), 1, map[string]TenantSecurityState{"acme": {}}), "stale")
	require.ErrorContains(t, provider.Replace(context.Background(), 2, map[string]TenantSecurityState{"acme": {Disabled: true}}), "conflicting")
}

func TestPostgresSecurityStateFailsClosedWithoutMigration(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	require.NoError(t, db.Exec(`DROP TABLE IF EXISTS gql_security_state, gql_security_revision`).Error)
	_, err := NewPostgresSecurityStateProvider(db)
	require.ErrorContains(t, err, "migration")
}

func TestPostgresSecurityStateFailsClosedWithoutSingleton(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	require.NoError(t, db.Exec(`CREATE TABLE gql_security_state (
		singleton boolean PRIMARY KEY, revision bigint NOT NULL, tenants jsonb NOT NULL
	)`).Error)
	_, err := NewPostgresSecurityStateProvider(db)
	require.ErrorContains(t, err, "migration")
}

func TestPostgresSecurityStateFailsClosedAfterSingletonDeletion(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	require.NoError(t, db.Exec(PostgresSecurityStateDDL).Error)
	provider, err := NewPostgresSecurityStateProvider(db)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`DELETE FROM gql_security_state WHERE singleton`).Error)
	_, err = provider.Revision(context.Background())
	require.Error(t, err)
	require.Error(t, provider.Replace(context.Background(), 1, map[string]TenantSecurityState{"acme": {}}))
}
