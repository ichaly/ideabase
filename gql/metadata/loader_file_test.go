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

func TestFileLoaderResolvesModesAndLoadsValidMetadata(t *testing.T) {
	root := t.TempDir()
	cfg := &internal.Config{}
	cfg.Root = root
	cfg.Mode = "dev"
	if got := ResolveMetadataPath(cfg); got != filepath.Join(root, "cfg", "metadata.dev.json") {
		t.Fatalf("default metadata path = %q", got)
	}
	cfg.Metadata.File = "cfg/metadata-{mode}.json"
	if got := ResolveMetadataPath(cfg); got != filepath.Join(root, "cfg", "metadata-dev.json") {
		t.Fatalf("custom metadata path = %q", got)
	}
	path := filepath.Join(root, "cfg", "metadata-dev.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"nodes":{"User":{"name":"User","table":"users","fields":{"id":{"name":"id","column":"id"}}}},"version":"v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newFakeHoster()
	if err := NewFileLoader(cfg).Load(h); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.GetNode("User"); !ok || h.version != "v1" {
		t.Fatalf("metadata file was not loaded: %#v", h)
	}
}
