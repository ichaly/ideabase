package gql

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
)

// PostgresSecurityStateDDL creates the single shared revocation snapshot used
// by every replica. Applications apply it with their normal migration tool.
const PostgresSecurityStateDDL = `CREATE TABLE IF NOT EXISTS gql_security_state (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  revision bigint NOT NULL CHECK (revision >= 0),
  tenants jsonb NOT NULL
);
INSERT INTO gql_security_state (singleton, revision, tenants)
VALUES (true, 0, '{}'::jsonb)
ON CONFLICT (singleton) DO NOTHING;`

// PostgresSecurityStateProvider is the production cluster-wide revocation
// boundary. Reads observe one transactionally published snapshot.
const DefaultPostgresSecurityMaxStaleness = 250 * time.Millisecond

type PostgresSecurityStateOption func(*PostgresSecurityStateProvider)

// WithPostgresSecurityMaxStaleness bounds how long a healthy last-known
// snapshot may authorize requests while refreshes are skipped or unavailable.
func WithPostgresSecurityMaxStaleness(window time.Duration) PostgresSecurityStateOption {
	return func(provider *PostgresSecurityStateProvider) {
		if window > 0 {
			provider.maxStaleness = window
		}
	}
}

type postgresSecuritySnapshot struct {
	revision uint64
	tenants  map[string]TenantSecurityState
	loadedAt time.Time
}

type PostgresSecurityStateProvider struct {
	db           *gorm.DB
	maxStaleness time.Duration
	state        atomic.Pointer[postgresSecuritySnapshot]
	refreshMu    sync.Mutex
}

func NewPostgresSecurityStateProvider(db *gorm.DB, options ...PostgresSecurityStateOption) (*PostgresSecurityStateProvider, error) {
	if db == nil {
		return nil, fmt.Errorf("postgres security state requires a database")
	}
	provider := &PostgresSecurityStateProvider{db: db, maxStaleness: DefaultPostgresSecurityMaxStaleness}
	for _, option := range options {
		if option != nil {
			option(provider)
		}
	}
	if err := provider.Ready(context.Background()); err != nil {
		return nil, err
	}
	return provider, nil
}

func (provider *PostgresSecurityStateProvider) Ready(ctx context.Context) error {
	if provider == nil || provider.db == nil {
		return fmt.Errorf("security state unavailable")
	}
	provider.refreshMu.Lock()
	defer provider.refreshMu.Unlock()
	var loaded *postgresSecuritySnapshot
	err := postgresPreflight(ctx, provider.db, "security state", func(tx *gorm.DB) error {
		var err error
		if loaded, err = loadSecuritySnapshot(tx); err != nil {
			return err
		}
		result := tx.Exec(`UPDATE gql_security_state SET revision=revision WHERE singleton`)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("singleton row is unavailable")
		}
		return nil
	})
	if err == nil && loaded != nil {
		provider.state.Store(loaded)
	}
	return err
}

func (provider *PostgresSecurityStateProvider) Revision(ctx context.Context) (uint64, error) {
	snapshot, err := provider.snapshot(ctx)
	if err != nil {
		return 0, fmt.Errorf("load security revision: %w", err)
	}
	return snapshot.revision, nil
}

func (provider *PostgresSecurityStateProvider) Replace(ctx context.Context, revision uint64, tenants map[string]TenantSecurityState) error {
	if provider == nil || provider.db == nil {
		return fmt.Errorf("security state unavailable")
	}
	provider.refreshMu.Lock()
	defer provider.refreshMu.Unlock()
	encoded, err := stdjson.Marshal(tenants)
	if err != nil {
		return fmt.Errorf("encode security state: %w", err)
	}
	var published *postgresSecuritySnapshot
	err = provider.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current struct {
			Revision uint64
			Tenants  []byte
		}
		result := tx.Raw(`SELECT revision, tenants FROM gql_security_state WHERE singleton FOR UPDATE`).Scan(&current)
		if result.Error != nil {
			return fmt.Errorf("load security state: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("security state singleton row is unavailable")
		}
		if revision < current.Revision {
			return fmt.Errorf("stale revocation revision %d", revision)
		}
		if revision == current.Revision {
			var existing map[string]TenantSecurityState
			if err := stdjson.Unmarshal(current.Tenants, &existing); err != nil {
				return fmt.Errorf("decode security state: %w", err)
			}
			if maps.Equal(existing, tenants) {
				published = &postgresSecuritySnapshot{revision: revision, tenants: cloneTenantStates(existing), loadedAt: time.Now()}
				return nil
			}
			return fmt.Errorf("conflicting revocation revision %d", revision)
		}
		result = tx.Exec(`UPDATE gql_security_state SET revision = ?, tenants = ?::jsonb WHERE singleton`, revision, string(encoded))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("security state singleton row is unavailable")
		}
		published = &postgresSecuritySnapshot{revision: revision, tenants: cloneTenantStates(tenants), loadedAt: time.Now()}
		return nil
	})
	if err == nil && published != nil {
		provider.state.Store(published)
	}
	return err
}

func (provider *PostgresSecurityStateProvider) Check(ctx context.Context, identity Identity, route TenantRoute) error {
	if route.Tenant != "" && route.Tenant != identity.Tenant() {
		return fmt.Errorf("tenant security state unavailable")
	}
	snapshot, err := provider.snapshot(ctx)
	if err != nil {
		return fmt.Errorf("load tenant security state: %w", err)
	}
	state, ok := snapshot.tenants[identity.Tenant()]
	if !ok {
		return fmt.Errorf("tenant security state unavailable")
	}
	return checkTenantSecurityState(identity, route, state)
}

func (provider *PostgresSecurityStateProvider) snapshot(ctx context.Context) (*postgresSecuritySnapshot, error) {
	if provider == nil || provider.db == nil {
		return nil, fmt.Errorf("security state unavailable")
	}
	now := time.Now()
	current := provider.state.Load()
	if current != nil && now.Sub(current.loadedAt) <= provider.maxStaleness {
		return current, nil
	}
	provider.refreshMu.Lock()
	defer provider.refreshMu.Unlock()
	now = time.Now()
	current = provider.state.Load()
	if current != nil && now.Sub(current.loadedAt) <= provider.maxStaleness {
		return current, nil
	}
	next, err := provider.loadSnapshot(ctx)
	if err == nil {
		provider.state.Store(next)
		return next, nil
	}
	if current != nil && now.Sub(current.loadedAt) <= provider.maxStaleness {
		return current, nil
	}
	return nil, fmt.Errorf("security state unavailable: %w", err)
}

func (provider *PostgresSecurityStateProvider) loadSnapshot(ctx context.Context) (*postgresSecuritySnapshot, error) {
	return loadSecuritySnapshot(provider.db.WithContext(ctx))
}

func loadSecuritySnapshot(db *gorm.DB) (*postgresSecuritySnapshot, error) {
	var state struct {
		Revision uint64
		Tenants  []byte
	}
	result := db.Raw(`SELECT revision, tenants FROM gql_security_state WHERE singleton`).Scan(&state)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 || len(state.Tenants) == 0 {
		return nil, fmt.Errorf("singleton row is unavailable")
	}
	var tenants map[string]TenantSecurityState
	if err := stdjson.Unmarshal(state.Tenants, &tenants); err != nil {
		return nil, fmt.Errorf("decode security state: %w", err)
	}
	return &postgresSecuritySnapshot{revision: state.Revision, tenants: cloneTenantStates(tenants), loadedAt: time.Now()}, nil
}

func checkTenantSecurityState(identity Identity, route TenantRoute, state TenantSecurityState) error {
	if state.Disabled {
		return &publicFailure{"tenant is disabled", "TENANT_DISABLED"}
	}
	if identity.PolicyRevision() < state.MinimumPolicyRevision {
		return &publicFailure{"identity policy revision is revoked", "POLICY_REVOKED"}
	}
	if identity.CredentialEpoch() < state.MinimumCredentialEpoch {
		return &publicFailure{"identity credential epoch is revoked", "CREDENTIAL_REVOKED"}
	}
	if route.Revision < state.MinimumRouteRevision {
		return &publicFailure{"tenant route revision is revoked", "ROUTE_REVOKED"}
	}
	return nil
}
