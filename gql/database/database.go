package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// DBTX is the transaction-aware SQL execution boundary. Both *sql.DB and
// *sql.Tx implement it.
type DBTX interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Connection binds an application-owned connection pool to its SQL dialect
// and optional out-of-band connection string.
type Connection struct {
	pool   *sql.DB
	driver string
	dsn    string
}

type Option func(*Connection)

func WithDSN(dsn string) Option {
	return func(connection *Connection) { connection.dsn = strings.TrimSpace(dsn) }
}

func New(pool *sql.DB, driver string, options ...Option) (*Connection, error) {
	connection := &Connection{pool: pool, driver: strings.TrimSpace(driver)}
	for _, option := range options {
		if option != nil {
			option(connection)
		}
	}
	if connection.pool == nil {
		return nil, fmt.Errorf("database connection pool is required")
	}
	if connection.driver == "" {
		return nil, fmt.Errorf("database driver is required")
	}
	return connection, nil
}

func (connection *Connection) Pool() *sql.DB {
	if connection == nil {
		return nil
	}
	return connection.pool
}

func (connection *Connection) Driver() string {
	if connection == nil {
		return ""
	}
	return connection.driver
}

func (connection *Connection) DSN() string {
	if connection == nil {
		return ""
	}
	return connection.dsn
}
