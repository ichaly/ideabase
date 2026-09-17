package std_test

import (
	"database/sql"
	"testing"

	"github.com/ichaly/ideabase/std"
	_ "github.com/ichaly/ideabase/std/postgres"
	"github.com/stretchr/testify/require"
)

func TestOpenConnectionPoolLifecycle(t *testing.T) {
	connection, err := std.OpenConnection("postgres://user:secret@localhost/app")
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	require.Zero(t, connection.Pool().Stats().OpenConnections)
	require.NoError(t, connection.Close())
	require.Error(t, connection.Pool().Ping())
}

func TestWrapConnectionReusesPool(t *testing.T) {
	pool, err := sql.Open("pgx", "postgres://user:secret@localhost/app")
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	connection, err := std.WrapConnection(pool, "postgres")
	require.NoError(t, err)
	require.Same(t, pool, connection.Pool())
	require.Equal(t, "postgres", connection.Driver())
	require.Empty(t, connection.DSN())
}

func TestWrapConnectionRejectsIncompleteInput(t *testing.T) {
	_, err := std.WrapConnection(nil, "postgres")
	require.ErrorContains(t, err, "connection pool")
	_, err = std.WrapConnection(&sql.DB{}, "")
	require.ErrorContains(t, err, "driver")
}

func TestOpenConnectionRejectsInvalidDSN(t *testing.T) {
	for _, dsn := range []string{"", "user=app password=secret", "unregistered://user:secret@host/db"} {
		_, err := std.OpenConnection(dsn)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret")
	}
}
