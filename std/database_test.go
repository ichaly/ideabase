package std_test

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/ichaly/ideabase/std"
	_ "github.com/ichaly/ideabase/std/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func init() { sql.Register("std_shared_pool_test", sharedPoolDriver{}) }

type sharedPoolDriver struct{}
type sharedPoolConn struct{}

func (sharedPoolDriver) Open(string) (driver.Conn, error)  { return sharedPoolConn{}, nil }
func (sharedPoolConn) Close() error                        { return nil }
func (sharedPoolConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (sharedPoolConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }

type poolPlugin struct{ pool *sql.DB }

func (*poolPlugin) Name() string { return "shared_pool_test" }
func (p *poolPlugin) Initialize(db *gorm.DB) error {
	var err error
	p.pool, err = db.DB()
	return err
}

func TestNewDatabaseUsesSharedPool(t *testing.T) {
	pool, err := sql.Open("std_shared_pool_test", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	connection, err := std.WrapConnection(pool, "postgres")
	require.NoError(t, err)
	plugin := &poolPlugin{}
	orm, err := std.NewDatabase(nil, []gorm.Plugin{plugin}, &std.Config{}, connection)
	require.NoError(t, err)
	actual, err := orm.DB()
	require.NoError(t, err)
	require.Same(t, pool, actual)
	require.Same(t, pool, plugin.pool)
	require.Equal(t, "postgres", orm.Dialector.Name())
	require.NoError(t, connection.Close())
	require.Error(t, actual.Ping())
}

func TestNewConnectionUsesConfiguredURL(t *testing.T) {
	config := &std.Config{}
	config.Database = "postgres://localhost/example?sslmode=disable"
	connection, err := std.NewConnection(config)
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	require.Equal(t, config.Database, connection.DSN())
	require.Equal(t, "postgres", connection.Driver())
	require.Equal(t, 90, connection.Pool().Stats().MaxOpenConnections)
	require.Zero(t, connection.Pool().Stats().OpenConnections)
}
