package gql

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestSearchProbeContextIsBounded(t *testing.T) {
	ctx, cancel := startupProbeContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	require.LessOrEqual(t, time.Until(deadline), startupProbeTimeout)
}

func TestSearchProbeOnlyTreatsPermissionDeniedAsFallback(t *testing.T) {
	require.True(t, isPermissionDenied(&pgconn.PgError{Code: "42501"}))
	require.False(t, isPermissionDenied(&pgconn.PgError{Code: "57014"}))
}

func TestSearchProbeRequiresDatabaseErrorsToPropagate(t *testing.T) {
	_, _, err := detectPostgresSearch(nil)
	require.Error(t, err)
}
