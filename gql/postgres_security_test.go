package gql

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPostgresSecurityStateIsSharedAcrossInstances(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	testExec(t, db, PostgresSecurityStateDDL)
	one, err := NewPostgresSecurityStateProvider(db)
	require.NoError(t, err)
	two, err := NewPostgresSecurityStateProvider(db, WithPostgresSecurityMaxStaleness(time.Millisecond))
	require.NoError(t, err)
	identity, err := NewIdentity(IdentityInput{Tenant: "acme", Subject: "user", PolicyRevision: 2, CredentialEpoch: 3})
	require.NoError(t, err)
	route := TenantRoute{Tenant: "acme", Revision: 4}

	require.NoError(t, one.Replace(context.Background(), 1, map[string]TenantSecurityState{
		"acme": {MinimumPolicyRevision: 2, MinimumCredentialEpoch: 3, MinimumRouteRevision: 4},
	}))
	time.Sleep(2 * time.Millisecond)
	require.NoError(t, two.Check(context.Background(), identity, route))
	require.NoError(t, one.Replace(context.Background(), 2, map[string]TenantSecurityState{
		"acme": {Disabled: true},
	}))
	time.Sleep(2 * time.Millisecond)
	require.ErrorContains(t, two.Check(context.Background(), identity, route), "disabled")
}

func TestPostgresSecurityStateUsesBoundedLocalSnapshot(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	testExec(t, db, PostgresSecurityStateDDL)
	one, err := NewPostgresSecurityStateProvider(db, WithPostgresSecurityMaxStaleness(20*time.Millisecond))
	require.NoError(t, err)
	two, err := NewPostgresSecurityStateProvider(db, WithPostgresSecurityMaxStaleness(20*time.Millisecond))
	require.NoError(t, err)
	identity, err := NewIdentity(IdentityInput{Tenant: "acme", Subject: "user"})
	require.NoError(t, err)
	route := TenantRoute{Tenant: "acme"}
	require.NoError(t, one.Replace(context.Background(), 1, map[string]TenantSecurityState{"acme": {}}))
	// The second instance may use its immutable snapshot during the bounded window.
	require.Error(t, two.Check(context.Background(), identity, route))
	time.Sleep(30 * time.Millisecond)
	require.NoError(t, two.Check(context.Background(), identity, route))
	require.NoError(t, one.Replace(context.Background(), 2, map[string]TenantSecurityState{"acme": {Disabled: true}}))
	require.ErrorContains(t, one.Check(context.Background(), identity, route), "disabled", "Replace must publish this instance immediately")
	time.Sleep(30 * time.Millisecond)
	require.ErrorContains(t, two.Check(context.Background(), identity, route), "disabled")
}

func TestPostgresSecurityStateRefreshesWithExactlyOneSelectPerWindow(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	testExec(t, db, PostgresSecurityStateDDL)
	one, err := NewPostgresSecurityStateProvider(db, WithPostgresSecurityMaxStaleness(10*time.Millisecond))
	require.NoError(t, err)
	two, err := NewPostgresSecurityStateProvider(db, WithPostgresSecurityMaxStaleness(10*time.Millisecond))
	require.NoError(t, err)
	identity, err := NewIdentity(IdentityInput{Tenant: "acme", Subject: "user"})
	require.NoError(t, err)
	require.NoError(t, one.Replace(context.Background(), 1, map[string]TenantSecurityState{"acme": {}}))
	time.Sleep(15 * time.Millisecond)
	require.NoError(t, two.Check(context.Background(), identity, TenantRoute{Tenant: "acme"}))
	refreshed := two.state.Load()
	require.EqualValues(t, 1, refreshed.revision)
	require.NoError(t, two.Check(context.Background(), identity, TenantRoute{Tenant: "acme"}))
	require.Same(t, refreshed, two.state.Load(), "subsequent checks within the window are local")
}

func TestPostgresSecurityStateConcurrentChecksAndReplaceAreRaceFree(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	testExec(t, db, PostgresSecurityStateDDL)
	provider, err := NewPostgresSecurityStateProvider(db, WithPostgresSecurityMaxStaleness(time.Millisecond))
	require.NoError(t, err)
	identity, err := NewIdentity(IdentityInput{Tenant: "acme", Subject: "user"})
	require.NoError(t, err)
	require.NoError(t, provider.Replace(context.Background(), 1, map[string]TenantSecurityState{"acme": {}}))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = provider.Check(context.Background(), identity, TenantRoute{Tenant: "acme"})
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for revision := uint64(2); revision < 12; revision++ {
			_ = provider.Replace(context.Background(), revision, map[string]TenantSecurityState{"acme": {Disabled: revision%2 == 0}})
		}
	}()
	wg.Wait()
}

func TestPostgresSecurityStateFailsClosedAfterSnapshotExpiresOnDatabaseFailure(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	testExec(t, db, PostgresSecurityStateDDL)
	provider, err := NewPostgresSecurityStateProvider(db, WithPostgresSecurityMaxStaleness(10*time.Millisecond))
	require.NoError(t, err)
	identity, err := NewIdentity(IdentityInput{Tenant: "acme", Subject: "user"})
	require.NoError(t, err)
	require.NoError(t, provider.Replace(context.Background(), 1, map[string]TenantSecurityState{"acme": {}}))
	require.NoError(t, db.Pool().Close())
	require.NoError(t, provider.Check(context.Background(), identity, TenantRoute{Tenant: "acme"}))
	time.Sleep(20 * time.Millisecond)
	require.ErrorContains(t, provider.Check(context.Background(), identity, TenantRoute{Tenant: "acme"}), "unavailable")
}

func TestPostgresSecurityStateRejectsStaleAndConflictingRevision(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	testExec(t, db, PostgresSecurityStateDDL)
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
	testExec(t, db, `DROP TABLE IF EXISTS gql_security_state, gql_security_revision`)
	_, err := NewPostgresSecurityStateProvider(db)
	require.ErrorContains(t, err, "migration")
}

func TestPostgresSecurityStateFailsClosedWithoutSingleton(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	testExec(t, db, `CREATE TABLE gql_security_state (
		singleton boolean PRIMARY KEY, revision bigint NOT NULL, tenants jsonb NOT NULL
	)`)
	_, err := NewPostgresSecurityStateProvider(db)
	require.ErrorContains(t, err, "migration")
}

func TestPostgresSecurityStateFailsClosedAfterSingletonDeletion(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	testExec(t, db, PostgresSecurityStateDDL)
	provider, err := NewPostgresSecurityStateProvider(db, WithPostgresSecurityMaxStaleness(5*time.Millisecond))
	require.NoError(t, err)
	testExec(t, db, `DELETE FROM gql_security_state WHERE singleton`)
	time.Sleep(10 * time.Millisecond)
	_, err = provider.Revision(context.Background())
	require.Error(t, err)
	require.Error(t, provider.Replace(context.Background(), 1, map[string]TenantSecurityState{"acme": {}}))
}
