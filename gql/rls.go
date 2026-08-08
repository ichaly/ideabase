package gql

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// RLSRole describes the database role used by shared-table execution.
// A production role must not bypass PostgreSQL row-level security.
type RLSRole struct {
	Superuser   bool
	BypassRLS   bool
	OwnerTables []string
}

// RLSTable is the trusted startup catalog view of a table requiring isolation.
type RLSTable struct {
	Name      string
	Protected bool
	HasPolicy bool
	Force     bool
}

type RLSConfig struct {
	Role   RLSRole
	Tables []RLSTable
}

// InspectRLS reads authoritative PostgreSQL role and table facts. Callers may
// use an explicit RLSConfig in tests, but production startup should use this
// catalog result rather than trusting configuration claims.
func InspectRLS(db *gorm.DB, metadata *Metadata) (RLSConfig, error) {
	if db == nil || metadata == nil || db.Name() != "postgres" {
		return RLSConfig{}, fmt.Errorf("PostgreSQL database and metadata are required for RLS inspection")
	}
	config := RLSConfig{}
	if err := db.Raw(`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`).Row().Scan(&config.Role.Superuser, &config.Role.BypassRLS); err != nil {
		return RLSConfig{}, fmt.Errorf("inspect RLS role: %w", err)
	}
	type catalogRow struct {
		Name                        string
		Protected, HasPolicy, Force bool
		Owned                       bool
	}
	var rows []catalogRow
	err := db.Raw(`
SELECT c.relname AS name, owner.rolname = current_user AS owned,
       c.relrowsecurity AS protected, c.relforcerowsecurity AS force,
       EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid = c.oid) AS has_policy
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_roles owner ON owner.oid = c.relowner
WHERE n.nspname = ? AND c.relkind IN ('r', 'p')
ORDER BY c.relname`, metadata.SchemaName()).Scan(&rows).Error
	if err != nil {
		return RLSConfig{}, fmt.Errorf("inspect RLS tables: %w", err)
	}
	for _, row := range rows {
		config.Tables = append(config.Tables, RLSTable{Name: row.Name, Protected: row.Protected, HasPolicy: row.HasPolicy, Force: row.Force})
		if row.Owned {
			config.Role.OwnerTables = append(config.Role.OwnerTables, row.Name)
		}
	}
	return config, nil
}

func ValidateRLS(config RLSConfig) error {
	if config.Role.Superuser {
		return fmt.Errorf("rls role must not be superuser")
	}
	if config.Role.BypassRLS {
		return fmt.Errorf("rls role must have NO BYPASSRLS")
	}
	owners := make(map[string]struct{}, len(config.Role.OwnerTables))
	for _, table := range config.Role.OwnerTables {
		if table == "" || strings.IndexByte(table, 0) >= 0 {
			return fmt.Errorf("invalid owner table")
		}
		owners[table] = struct{}{}
	}
	seen := make(map[string]struct{}, len(config.Tables))
	for _, table := range config.Tables {
		if table.Name == "" || strings.IndexByte(table.Name, 0) >= 0 {
			return fmt.Errorf("invalid rls table name")
		}
		if _, ok := seen[table.Name]; ok {
			return fmt.Errorf("duplicate rls table: %s", table.Name)
		}
		seen[table.Name] = struct{}{}
		if !table.Protected {
			continue
		}
		if !table.HasPolicy && !table.Force {
			return fmt.Errorf("protected table %s has no rls policy or FORCE ROW LEVEL SECURITY", table.Name)
		}
		if _, owner := owners[table.Name]; owner && !table.Force {
			return fmt.Errorf("rls role owns protected table %s", table.Name)
		}
	}
	return nil
}

// RLSStatement is a parameterized transaction-local identity operation.
// The caller must execute it on the transaction before any protected SQL.
type RLSStatement struct {
	SQL  string
	Args []any
}

func NewRLSIdentityStatement(identity Identity) (RLSStatement, error) {
	if identity.Tenant() == "" || identity.Subject() == "" {
		return RLSStatement{}, fmt.Errorf("verified identity is required")
	}
	return RLSStatement{
		SQL:  "SELECT set_config($1, $2, true), set_config($3, $4, true), set_config($5, $6, true)",
		Args: []any{"gql.tenant", identity.Tenant(), "gql.subject", identity.Subject(), "gql.actor", identity.Actor()},
	}, nil
}

func (my *Executor) prepareRLS(ctx context.Context, tx *gorm.DB) error {
	identity, ok := IdentityFromContext(ctx)
	if !ok {
		return fmt.Errorf("verified identity is required for RLS")
	}
	statement, err := NewRLSIdentityStatement(identity)
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(statement.SQL, statement.Args...).Error
}
