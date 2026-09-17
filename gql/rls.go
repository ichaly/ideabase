package gql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/std"
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
func InspectRLS(db *std.Connection, metadata *Metadata) (RLSConfig, error) {
	if db == nil || metadata == nil || db.Driver() != "postgres" {
		return RLSConfig{}, fmt.Errorf("PostgreSQL database and metadata are required for RLS inspection")
	}
	ctx, cancel := startupProbeContext(context.Background())
	defer cancel()
	config := RLSConfig{}
	if err := db.Pool().QueryRowContext(ctx, `SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&config.Role.Superuser, &config.Role.BypassRLS); err != nil {
		return RLSConfig{}, fmt.Errorf("inspect RLS role: %w", err)
	}
	type catalogRow struct {
		Name                        string
		Protected, HasPolicy, Force bool
		Owned                       bool
	}
	rows, err := db.Pool().QueryContext(ctx, `
SELECT c.relname AS name, owner.rolname = current_user AS owned,
       c.relrowsecurity AS protected, c.relforcerowsecurity AS force,
       EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid = c.oid) AS has_policy
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_roles owner ON owner.oid = c.relowner
WHERE n.nspname = $1 AND c.relkind IN ('r', 'p')
ORDER BY c.relname`, metadata.SchemaName())
	if err != nil {
		return RLSConfig{}, fmt.Errorf("inspect RLS tables: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row catalogRow
		if err := rows.Scan(&row.Name, &row.Owned, &row.Protected, &row.Force, &row.HasPolicy); err != nil {
			return RLSConfig{}, fmt.Errorf("inspect RLS tables: %w", err)
		}
		config.Tables = append(config.Tables, RLSTable{Name: row.Name, Protected: row.Protected, HasPolicy: row.HasPolicy, Force: row.Force})
		if row.Owned {
			config.Role.OwnerTables = append(config.Role.OwnerTables, row.Name)
		}
	}
	if err := rows.Err(); err != nil {
		return RLSConfig{}, fmt.Errorf("inspect RLS tables: %w", err)
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

type rlsPreparedKey struct{}

func rlsPrepared(ctx context.Context, tx *sql.Tx) bool {
	prepared, _ := ctx.Value(rlsPreparedKey{}).(*sql.Tx)
	return prepared != nil && prepared == tx
}

func markRLSPrepared(ctx context.Context, tx *sql.Tx) context.Context {
	return context.WithValue(ctx, rlsPreparedKey{}, tx)
}

func NewRLSIdentityStatement(identity Identity) (RLSStatement, error) {
	if identity.Tenant() == "" || identity.Subject() == "" {
		return RLSStatement{}, &publicFailure{"verified identity is required", "IDENTITY_REQUIRED"}
	}
	return RLSStatement{
		SQL:  "SELECT set_config($1, $2, true), set_config($3, $4, true), set_config($5, $6, true)",
		Args: []any{"gql.tenant", identity.Tenant(), "gql.subject", identity.Subject(), "gql.actor", identity.Actor()},
	}, nil
}

func (my *Engine) prepareRLS(ctx context.Context, tx *sql.Tx) error {
	if rlsPrepared(ctx, tx) {
		return nil
	}
	identity, ok := IdentityFromContext(ctx)
	if !ok {
		return &publicFailure{"verified identity is required for RLS", "IDENTITY_REQUIRED"}
	}
	statement, err := NewRLSIdentityStatement(identity)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, statement.SQL, statement.Args...)
	return err
}
