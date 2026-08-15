package gql

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"maps"

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
type PostgresSecurityStateProvider struct{ db *gorm.DB }

func NewPostgresSecurityStateProvider(db *gorm.DB) (*PostgresSecurityStateProvider, error) {
	if db == nil {
		return nil, fmt.Errorf("postgres security state requires a database")
	}
	provider := &PostgresSecurityStateProvider{db: db}
	if err := provider.Ready(context.Background()); err != nil {
		return nil, err
	}
	return provider, nil
}

func (provider *PostgresSecurityStateProvider) Ready(ctx context.Context) error {
	if provider == nil || provider.db == nil {
		return fmt.Errorf("security state unavailable")
	}
	return postgresPreflight(ctx, provider.db, "security state", func(tx *gorm.DB) error {
		var state struct {
			Revision uint64
			Tenants  []byte
		}
		if err := tx.Raw(`SELECT revision, tenants FROM gql_security_state WHERE singleton`).Take(&state).Error; err != nil {
			return err
		}
		if len(state.Tenants) == 0 {
			return fmt.Errorf("singleton row is unavailable")
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
}

func (provider *PostgresSecurityStateProvider) Revision(ctx context.Context) (uint64, error) {
	var revision uint64
	if provider == nil || provider.db == nil {
		return 0, fmt.Errorf("security state unavailable")
	}
	result := provider.db.WithContext(ctx).Raw(`SELECT revision FROM gql_security_state WHERE singleton`).Scan(&revision)
	if result.Error != nil {
		return 0, fmt.Errorf("load security revision: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return 0, fmt.Errorf("load security revision: singleton row is unavailable")
	}
	return revision, nil
}

func (provider *PostgresSecurityStateProvider) Replace(ctx context.Context, revision uint64, tenants map[string]TenantSecurityState) error {
	encoded, err := stdjson.Marshal(tenants)
	if err != nil {
		return fmt.Errorf("encode security state: %w", err)
	}
	return provider.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		return nil
	})
}

func (provider *PostgresSecurityStateProvider) Check(ctx context.Context, identity Identity, route TenantRoute) error {
	if route.Tenant != "" && route.Tenant != identity.Tenant() {
		return fmt.Errorf("tenant security state unavailable")
	}
	var encoded string
	if err := provider.db.WithContext(ctx).Raw(`SELECT tenants -> ? FROM gql_security_state WHERE singleton`, identity.Tenant()).Scan(&encoded).Error; err != nil {
		return fmt.Errorf("load tenant security state: %w", err)
	}
	if encoded == "" || encoded == "null" {
		return fmt.Errorf("tenant security state unavailable")
	}
	var state TenantSecurityState
	if err := stdjson.Unmarshal([]byte(encoded), &state); err != nil {
		return fmt.Errorf("decode tenant security state: %w", err)
	}
	return checkTenantSecurityState(identity, route, state)
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
