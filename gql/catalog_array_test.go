package gql

import (
	"context"
	"testing"

	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
)

func TestPostgresArrayMutationRoundTrip(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	require.NoError(t, db.Exec(`CREATE TABLE array_records (
		id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		tags text[] NOT NULL
	)`).Error)

	k, err := std.NewKonfig()
	require.NoError(t, err)
	k.Set("mode", "dev")
	k.Set("app.root", t.TempDir())
	k.Set("schema.schema", "public")
	meta, err := NewMetadata(k, db)
	require.NoError(t, err)
	compile, err := NewCompiler(meta, nil)
	require.NoError(t, err)
	executor, err := newEngineForTest(db, NewRenderer(meta), meta, compile)
	require.NoError(t, err)

	reply := executor.run(context.Background(), `mutation {
		createArrayRecord(input: {tags: ["fast", "safe"]}) { id tags }
	}`, nil, "")
	require.Empty(t, reply.Errors)
	require.Equal(t, []interface{}{"fast", "safe"}, reply.Data["createArrayRecord"].(map[string]interface{})["tags"])
}
