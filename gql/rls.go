package gql

import (
	"fmt"
	"strings"
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
