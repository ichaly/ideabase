package gql

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm/logger"
)

type postgresSecuritySelectCounter struct {
	logger.Interface
	mu      sync.Mutex
	selects int
}

func (counter *postgresSecuritySelectCounter) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sql)), "SELECT") {
		counter.mu.Lock()
		counter.selects++
		counter.mu.Unlock()
	}
	counter.Interface.Trace(ctx, begin, fc, err)
}

func (counter *postgresSecuritySelectCounter) Count() int {
	counter.mu.Lock()
	defer counter.mu.Unlock()
	return counter.selects
}

func TestPostgresSecurityStateIsSharedAcrossInstances(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	require.NoError(t, db.Exec(PostgresSecurityStateDDL).Error)
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
	require.NoError(t, db.Exec(PostgresSecurityStateDDL).Error)
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
	require.NoError(t, db.Exec(PostgresSecurityStateDDL).Error)
	one, err := NewPostgresSecurityStateProvider(db, WithPostgresSecurityMaxStaleness(10*time.Millisecond))
	require.NoError(t, err)
	two, err := NewPostgresSecurityStateProvider(db, WithPostgresSecurityMaxStaleness(10*time.Millisecond))
	require.NoError(t, err)
	identity, err := NewIdentity(IdentityInput{Tenant: "acme", Subject: "user"})
	require.NoError(t, err)
	require.NoError(t, one.Replace(context.Background(), 1, map[string]TenantSecurityState{"acme": {}}))
	counter := &postgresSecuritySelectCounter{Interface: db.Logger}
	db.Logger = counter
	time.Sleep(15 * time.Millisecond)
	require.NoError(t, two.Check(context.Background(), identity, TenantRoute{Tenant: "acme"}))
	require.NoError(t, two.Check(context.Background(), identity, TenantRoute{Tenant: "acme"}))
	require.Equal(t, 1, counter.Count(), "a stale snapshot refresh is one SELECT and subsequent checks are local")
}

func TestPostgresSecurityStateConcurrentChecksAndReplaceAreRaceFree(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	require.NoError(t, db.Exec(PostgresSecurityStateDDL).Error)
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
	require.NoError(t, db.Exec(PostgresSecurityStateDDL).Error)
	provider, err := NewPostgresSecurityStateProvider(db, WithPostgresSecurityMaxStaleness(10*time.Millisecond))
	require.NoError(t, err)
	identity, err := NewIdentity(IdentityInput{Tenant: "acme", Subject: "user"})
	require.NoError(t, err)
	require.NoError(t, provider.Replace(context.Background(), 1, map[string]TenantSecurityState{"acme": {}}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	require.NoError(t, provider.Check(context.Background(), identity, TenantRoute{Tenant: "acme"}))
	time.Sleep(20 * time.Millisecond)
	require.ErrorContains(t, provider.Check(context.Background(), identity, TenantRoute{Tenant: "acme"}), "unavailable")
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
	provider, err := NewPostgresSecurityStateProvider(db, WithPostgresSecurityMaxStaleness(5*time.Millisecond))
	require.NoError(t, err)
	require.NoError(t, db.Exec(`DELETE FROM gql_security_state WHERE singleton`).Error)
	time.Sleep(10 * time.Millisecond)
	_, err = provider.Revision(context.Background())
	require.Error(t, err)
	require.Error(t, provider.Replace(context.Background(), 1, map[string]TenantSecurityState{"acme": {}}))
}
