package pgsql

import (
	"testing"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/stretchr/testify/require"
)

func TestPresetKeepsReservedLinkColumnUnique(t *testing.T) {
	// Link INSERT reserves both FK columns before assigning their value writers.
	row := inputRow{columns: []string{"tenant_id", "target_id"}, values: map[string]paramWriter{}}
	setInputValue(&row, "tenant_id", func(*compiler.Context) error { return nil })
	setInputValue(&row, "updated_by", func(*compiler.Context) error { return nil })
	require.Equal(t, []string{"tenant_id", "target_id", "updated_by"}, row.columns)
	require.NotNil(t, row.values["tenant_id"])
}
