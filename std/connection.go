package std

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// DBTX is the transaction-aware SQL execution boundary shared by sql.DB and sql.Tx.
type DBTX interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Connection holds an application-owned pool, its dialect and connection string.
type Connection struct {
	pool   *sql.DB
	driver string
	dsn    string
}

// WrapConnection binds an application-owned pool to a SQL dialect.
func WrapConnection(pool *sql.DB, driver string) (*Connection, error) {
	driver = strings.TrimSpace(driver)
	if pool == nil {
		return nil, fmt.Errorf("database connection pool is required")
	}
	if driver == "" {
		return nil, fmt.Errorf("database driver is required")
	}
	return &Connection{pool: pool, driver: driver}, nil
}

// OpenConnection opens a pool without pinging it. Import the corresponding
// std driver package to register URL parsing and SQL/GORM drivers.
func OpenConnection(rawDSN string) (*Connection, error) {
	rawDSN = strings.TrimSpace(rawDSN)
	if rawDSN == "" {
		return nil, fmt.Errorf("database DSN is required")
	}
	scheme, _, hasScheme := strings.Cut(rawDSN, "://")
	if !hasScheme {
		return nil, fmt.Errorf("database DSN must include a supported URL scheme")
	}
	d, ok := dialectors[strings.ToLower(scheme)]
	if !ok {
		return nil, fmt.Errorf("database URL scheme is not registered; import its std driver package")
	}
	pool, dsn, err := d.Open(rawDSN)
	if err != nil {
		return nil, err
	}
	return &Connection{pool: pool, driver: d.name, dsn: dsn}, nil
}

func (c *Connection) Pool() *sql.DB {
	if c == nil {
		return nil
	}
	return c.pool
}
func (c *Connection) Driver() string {
	if c == nil {
		return ""
	}
	return c.driver
}
func (c *Connection) DSN() string {
	if c == nil {
		return ""
	}
	return c.dsn
}

// Close releases the underlying pool. The application owns the pool
// lifecycle and should close it after all shared users stop.
func (c *Connection) Close() error {
	if c == nil || c.pool == nil {
		return nil
	}
	return c.pool.Close()
}

// NewConnection creates a shared connection configured from application settings.
func NewConnection(c *Config) (*Connection, error) {
	db, err := OpenConnection(c.Database)
	if err != nil {
		return nil, err
	}
	db.Pool().SetMaxIdleConns(5)
	db.Pool().SetMaxOpenConns(90)
	db.Pool().SetConnMaxLifetime(5 * time.Minute)
	return db, nil
}
