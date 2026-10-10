package metadata

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The real catalog contract is covered by gql/catalog_metadata_test.go. These
// assertions protect the few SQL properties whose accidental removal would
// make quoted, composite, or expression-backed facts lossy.
func TestPgsqlMetaSQLUsesPositionalCatalogFacts(t *testing.T) {
	require.Contains(t, pgsqlMetaSQL, "JOIN pg_attribute a ON a.attrelid = r.relation_id")
	require.Contains(t, pgsqlMetaSQL, "a.attnum > 0 AND NOT a.attisdropped")
	require.Contains(t, pgsqlMetaSQL, "unnest(con.conkey) WITH ORDINALITY")
	require.Contains(t, pgsqlMetaSQL, "unnest(con.confkey) WITH ORDINALITY")
	require.Contains(t, pgsqlMetaSQL, "pg_get_indexdef(i.indexrelid, pos + 1, true)")
	require.Contains(t, pgsqlMetaSQL, "pg_get_expr(i.indpred, i.indrelid)")
	require.Contains(t, pgsqlMetaSQL, "ORDER BY e.enumsortorder")
	require.NotContains(t, pgsqlMetaSQL, "information_schema.columns")
}
