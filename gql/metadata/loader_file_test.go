package metadata

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/stretchr/testify/require"
)

func TestFileLoaderRejectsNilNodeWithCoordinate(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "cfg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "cfg", "metadata.json"), []byte(`{"nodes":{"User":null}}`), 0o600))
	cfg := &internal.Config{}
	cfg.Root = root
	err := NewFileLoader(cfg).Load(newFakeHoster())
	require.Error(t, err)
	require.Contains(t, err.Error(), "metadata.nodes.User")
}
