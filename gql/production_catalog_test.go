package gql

import (
	"testing"

	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
)

func TestProductionLoadsDatabaseCatalogWithoutGeneratedArtifact(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	k, err := std.NewKonfig()
	require.NoError(t, err)
	k.Set("mode", "prod")
	k.Set("app.root", t.TempDir())
	meta, err := NewMetadata(k, db)
	require.NoError(t, err, "a production database is the default metadata source")
	require.NotNil(t, meta.Nodes["User"])
	require.NotEmpty(t, meta.Nodes["User"].Fields)
}

func TestProductionConfigurationCanBuildMultipleCatalogs(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	k, err := std.NewKonfig()
	require.NoError(t, err)
	k.Set("mode", "prod")
	k.Set("app.root", t.TempDir())
	first, err := NewMetadata(k, db)
	require.NoError(t, err)
	second, err := NewMetadata(k, db)
	require.NoError(t, err, "defaults must not become an explicitly configured artifact")
	require.Equal(t, len(first.Nodes), len(second.Nodes))
}
