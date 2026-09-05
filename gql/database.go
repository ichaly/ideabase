package gql

import (
	"database/sql"

	sqldb "github.com/ichaly/ideabase/gql/database"
)

type DBTX = sqldb.DBTX
type Database = sqldb.Connection
type DatabaseOption = sqldb.Option

// WithDatabaseDSN supplies the connection string used by facilities such as
// PostgreSQL logical replication that require a separate connection.
func WithDatabaseDSN(dsn string) DatabaseOption {
	return sqldb.WithDSN(dsn)
}

// NewDatabase wraps an existing database/sql pool without cloning it.
func NewDatabase(pool *sql.DB, driver string, options ...DatabaseOption) (*Database, error) {
	return sqldb.New(pool, driver, options...)
}
