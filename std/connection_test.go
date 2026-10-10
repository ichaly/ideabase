package std_test

import (
	"testing"

	"github.com/ichaly/ideabase/std"
	_ "github.com/ichaly/ideabase/std/postgres"
	"github.com/stretchr/testify/require"
)

func TestNewConnectionPoolLifecycle(t *testing.T) {
	config := &std.Config{}
	config.Database = "postgres://user:secret@localhost/app"
	connection, err := std.NewConnection(config)
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	require.Zero(t, connection.Pool().Stats().OpenConnections)
	require.NoError(t, connection.Close())
	require.Error(t, connection.Pool().Ping())
}

func TestNewConnectionRejectsInvalidDSN(t *testing.T) {
	for _, dsn := range []string{"", "user=app password=secret", "unregistered://user:secret@host/db", "postgresql://user:secret@host/db"} {
		config := &std.Config{}
		config.Database = dsn
		_, err := std.NewConnection(config)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret")
	}
}
