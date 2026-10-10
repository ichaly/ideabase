package gql

import (
	"path/filepath"
	"testing"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
)

func TestRoutineCatalogSurvivesMetadataArtifact(t *testing.T) {
	root := t.TempDir()
	source := &Metadata{
		Nodes: map[string]*protocol.Class{"Account": {
			Name: "Account", Table: "account", PrimaryKeys: []string{"id"},
			Fields: map[string]*protocol.Field{"id": {Name: "id", Column: "id", Type: "ID", IsPrimary: true}},
		}}, Version: "catalog-v1",
		Routines: map[string]*protocol.Routine{"public.lookup(id integer)": {
			Schema: "public", Name: "lookup", Kind: protocol.RoutineFunction,
			Identity: "id integer", Arguments: []protocol.RoutineArgument{{Name: "id", Mode: protocol.RoutineArgIn, Type: "integer"}},
			ReturnType: "text", Volatility: protocol.RoutineVolatilityStable,
		}},
	}
	require.NoError(t, source.saveToFile(filepath.Join(root, "cfg", "metadata.test.json")))

	k, err := std.NewKonfig()
	require.NoError(t, err)
	k.Set("mode", "test")
	k.Set("app.root", root)
	loaded, err := NewMetadata(k, nil)
	require.NoError(t, err)
	require.Equal(t, source.Routines, loaded.Routines)
}

func TestCatalogFingerprintSeparatesShapeFromTenantSchema(t *testing.T) {
	build := func(schema, defaultValue string) *Metadata {
		field := &protocol.Field{Name: "id", Column: "id", Type: "int8", IsPrimary: true, Catalog: &protocol.Column{
			Type: protocol.TypeRef{Schema: schema, Name: "tenant_id", Kind: protocol.TypeDomain, Base: "int8"}, Default: &protocol.ColumnDefault{Expression: `nextval('"` + schema + `".account_id_seq'::regclass) + ` + defaultValue},
		}}
		class := &protocol.Class{Name: "Account", Table: "account", Schema: schema, TableKind: protocol.TableBase, PrimaryKeys: []string{"id"}, Fields: map[string]*protocol.Field{"id": field}}
		routine := &protocol.Routine{Schema: schema, Name: "lookup", Identity: `id "` + schema + `".tenant_id`, ReturnType: `"` + schema + `".tenant_id`, Arguments: []protocol.RoutineArgument{{Name: "id", Type: `"` + schema + `".tenant_id`}}}
		return &Metadata{Nodes: map[string]*protocol.Class{"Account": class}, Routines: map[string]*protocol.Routine{schema + ".lookup": routine}}
	}
	a, err := build("tenant_a", "1").CatalogFingerprint()
	require.NoError(t, err)
	b, err := build("tenant_b", "1").CatalogFingerprint()
	require.NoError(t, err)
	changed, err := build("tenant_b", "2").CatalogFingerprint()
	require.NoError(t, err)
	require.Equal(t, a, b, "tenant schema标识符不属于结构shape")
	require.NotEqual(t, a, changed, "default变化必须失效结构缓存")
}

func TestCatalogFingerprintIgnoresUnquotedTenantSchema(t *testing.T) {
	build := func(schema, literal string) *Metadata {
		field := &protocol.Field{Name: "id", Column: "id", Type: "int8", Catalog: &protocol.Column{
			Type:    protocol.TypeRef{Schema: schema, Name: "tenant_id", Kind: protocol.TypeDomain, Base: "int8"},
			Default: &protocol.ColumnDefault{Expression: "nextval('" + schema + ".account_id_seq'::regclass) || '" + literal + "'"},
		}}
		class := &protocol.Class{Name: "Account", Table: "account", Schema: schema, Fields: map[string]*protocol.Field{"id": field}}
		return &Metadata{Nodes: map[string]*protocol.Class{"Account": class}}
	}
	a, err := build("tenant_a", "tenant_a.status").CatalogFingerprint()
	require.NoError(t, err)
	b, err := build("tenant_b", "tenant_a.status").CatalogFingerprint()
	require.NoError(t, err)
	require.Equal(t, a, b)
	different, err := build("tenant_b", "tenant_b.status").CatalogFingerprint()
	require.NoError(t, err)
	require.NotEqual(t, a, different, "普通字符串字面量不是catalog标识符，必须保留")
}
