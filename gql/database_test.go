package gql

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewDatabasePreservesSharedPoolAndConnectionMetadata(t *testing.T) {
	pool := &sql.DB{}
	database, err := NewDatabase(pool, "postgres", WithDatabaseDSN("postgres://demo"))

	require.NoError(t, err)
	require.Same(t, pool, database.Pool())
	require.Equal(t, "postgres", database.Driver())
	require.Equal(t, "postgres://demo", database.DSN())
}

func TestNewDatabaseRejectsIncompleteConnection(t *testing.T) {
	pool := &sql.DB{}

	_, err := NewDatabase(nil, "postgres")
	require.ErrorContains(t, err, "connection pool")

	_, err = NewDatabase(pool, "")
	require.ErrorContains(t, err, "driver")
}
