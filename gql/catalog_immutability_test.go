package gql

import (
	"testing"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/require"
)

type retainedCatalogLoader struct{ class *protocol.Class }

func (l *retainedCatalogLoader) Name() string  { return "retained-catalog" }
func (l *retainedCatalogLoader) Support() bool { return true }
func (l *retainedCatalogLoader) Priority() int { return 1 }
func (l *retainedCatalogLoader) Load(host protocol.Hoster) error {
	l.class = &protocol.Class{Name: "User", Table: "users", Fields: map[string]*protocol.Field{
		"id": {Name: "id", Column: "id", Type: "integer", IsPrimary: true},
	}}
	return host.PutNode(l.class.Name, l.class)
}

func TestBuilderDoesNotServeLoaderOwnedCatalog(t *testing.T) {
	loader := &retainedCatalogLoader{}
	builder := New(snapshotKonfig(t), nil, snapshotOptions(loader)...).testAllowAllPolicy()
	engine, err := builder.Build()
	require.NoError(t, err)
	t.Cleanup(engine.Close)
	loader.class.Table = "forged_users"
	loader.class.Fields["id"].Column = "forged_id"
	require.Equal(t, "users", engine.metadata.Nodes["User"].Table)
	require.Equal(t, "id", engine.metadata.Nodes["User"].Fields["id"].Column)
}

// Engine owns one immutable catalog. Mutating the builder metadata after
// construction must not change planning, rendering, policy, or fingerprints.
func TestEngineFreezesMetadataCatalog(t *testing.T) {
	meta := createMockMetadata(t)
	compiler, err := NewCompiler(meta, nil)
	require.NoError(t, err)
	executor, err := newEngineForTest(nil, NewRenderer(meta), meta, compiler)
	require.NoError(t, err)

	meta.Nodes["User"].Table = "forged_users"
	delete(meta.Nodes["User"].Fields, "name")
	delete(meta.operations, "Query.users")

	require.Equal(t, "users", executor.metadata.Nodes["User"].Table)
	require.Contains(t, executor.metadata.Nodes["User"].Fields, "name")
	require.Contains(t, executor.metadata.operations, "Query.users")
}
