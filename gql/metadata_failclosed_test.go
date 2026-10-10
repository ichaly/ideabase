package gql

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ichaly/ideabase/gql/metadata"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
)

type failClosedLoader struct {
	name string
	err  error
}

func (l failClosedLoader) Name() string               { return l.name }
func (l failClosedLoader) Priority() int              { return 1 }
func (l failClosedLoader) Support() bool              { return true }
func (l failClosedLoader) Load(protocol.Hoster) error { return l.err }

type successLoader struct{}

func (successLoader) Name() string  { return "base" }
func (successLoader) Priority() int { return 0 }
func (successLoader) Support() bool { return true }
func (successLoader) Load(h protocol.Hoster) error {
	return h.PutNode("Base", &protocol.Class{Name: "Base", Fields: map[string]*protocol.Field{}})
}

var retainedClass *protocol.Class

type retainingLoader struct{}

func (retainingLoader) Name() string  { return "retaining" }
func (retainingLoader) Priority() int { return 0 }
func (retainingLoader) Support() bool { return true }
func (retainingLoader) Load(h protocol.Hoster) error {
	retainedClass = &protocol.Class{
		Name: "Base", Table: "base", Permissions: map[string]*protocol.Permission{
			"member": {Select: []string{"name"}},
		}, Fields: map[string]*protocol.Field{
			"name": {Name: "name", Column: "name", Type: "String", Relation: &protocol.Relation{Type: protocol.MANY_TO_ONE, SourceClass: "Base", TargetClass: "Owner", SourceField: "ownerId", TargetField: "id"}},
		},
	}
	return h.PutNode("Base", retainedClass)
}

func failClosedKonfig(t *testing.T) *std.Konfig {
	t.Helper()
	k, err := std.NewKonfig()
	require.NoError(t, err)
	k.Set("mode", "test")
	k.Set("app.root", t.TempDir())
	return k
}

func failClosedOptions(loader protocol.Loader) []MetadataOption {
	return []MetadataOption{
		WithoutLoader(metadata.LoaderPgsql, metadata.LoaderMysql, metadata.LoaderFile, metadata.LoaderConfig),
		WithLoader(successLoader{}),
		WithLoader(loader),
	}
}

func TestNewMetadataRequiredLoaderFailureFailsClosed(t *testing.T) {
	for _, name := range []string{metadata.LoaderConfig, metadata.LoaderFile, "explicit"} {
		t.Run(name, func(t *testing.T) {
			meta, err := NewMetadata(failClosedKonfig(t), nil, failClosedOptions(failClosedLoader{
				name: name,
				err:  errors.New("loader failed"),
			})...)
			require.Error(t, err)
			require.Nil(t, meta)
		})
	}
}

func TestNewMetadataOptionalLoaderFailureIsExplicitlyAllowed(t *testing.T) {
	meta, err := NewMetadata(failClosedKonfig(t), nil,
		WithoutLoader(metadata.LoaderPgsql, metadata.LoaderMysql, metadata.LoaderFile, metadata.LoaderConfig),
		WithLoader(successLoader{}),
		WithOptionalLoader(failClosedLoader{name: "optional", err: errors.New("loader failed")}),
	)
	require.NoError(t, err)
	require.NotNil(t, meta)
	require.Contains(t, meta.Nodes, "Base")
}

func TestNewMetadataExplicitFileConfigurationIsRequired(t *testing.T) {
	k := failClosedKonfig(t)
	k.Set("metadata.file", "missing.json")
	meta, err := NewMetadata(k, nil,
		WithoutLoader(metadata.LoaderPgsql, metadata.LoaderMysql, metadata.LoaderConfig),
		WithLoader(successLoader{}),
	)
	require.Error(t, err)
	require.Nil(t, meta)
}

func TestNewMetadataExplicitFileConfigurationIsRequiredInDev(t *testing.T) {
	k := failClosedKonfig(t)
	k.Set("mode", "dev")
	path := filepath.Join(t.TempDir(), "metadata.json")
	require.NoError(t, os.WriteFile(path, []byte("{"), 0o600))
	k.Set("metadata.file", path)
	meta, err := NewMetadata(k, nil,
		WithoutLoader(metadata.LoaderPgsql, metadata.LoaderMysql, metadata.LoaderConfig),
		WithLoader(successLoader{}),
	)
	require.Error(t, err)
	require.Nil(t, meta)
	var loaderErr *LoaderError
	require.ErrorAs(t, err, &loaderErr)
	require.Equal(t, metadata.LoaderFile, loaderErr.Loader)
	require.False(t, loaderErr.Optional)
}

func TestNewMetadataAuthoritativeLoaderCannotBeOptional(t *testing.T) {
	for _, name := range []string{metadata.LoaderConfig, metadata.LoaderFile} {
		t.Run(name, func(t *testing.T) {
			k := failClosedKonfig(t)
			if name == metadata.LoaderFile {
				k.Set("metadata.file", filepath.Join(t.TempDir(), "metadata.json"))
			}
			_, err := NewMetadata(k, nil,
				WithoutLoader(metadata.LoaderPgsql, metadata.LoaderMysql, metadata.LoaderFile, metadata.LoaderConfig),
				WithLoader(successLoader{}),
				WithOptionalLoader(failClosedLoader{name: name, err: errors.New("ignored")}),
			)
			require.Error(t, err)
			require.ErrorContains(t, err, "optional")
		})
	}
}

func TestNewMetadataClonesLoaderOwnedNodes(t *testing.T) {
	retainedClass = nil
	meta, err := NewMetadata(failClosedKonfig(t), nil,
		WithoutLoader(metadata.LoaderPgsql, metadata.LoaderMysql, metadata.LoaderFile, metadata.LoaderConfig),
		WithLoader(retainingLoader{}),
	)
	require.NoError(t, err)
	require.NotNil(t, retainedClass)
	cloned := meta.Nodes["Base"]
	require.NotSame(t, retainedClass, cloned)
	retainedClass.Name = "mutated"
	retainedClass.Fields["name"].Name = "mutated"
	retainedClass.Permissions["member"].Select[0] = "mutated"
	retainedClass.Fields["name"].Relation.SourceClass = "mutated"
	require.Equal(t, "Base", cloned.Name)
	require.Equal(t, "name", cloned.Fields["name"].Name)
	require.Equal(t, "name", cloned.Permissions["member"].Select[0])
	require.Equal(t, "Base", cloned.Fields["name"].Relation.SourceClass)
}
