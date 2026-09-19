package gql

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/ichaly/ideabase/gql/compiler/pgsql"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
)

type snapshotLoader struct{ governed bool }

func (snapshotLoader) Name() string  { return "snapshot" }
func (snapshotLoader) Support() bool { return true }
func (snapshotLoader) Priority() int { return 1 }
func (my snapshotLoader) Load(host protocol.Hoster) error {
	class := &protocol.Class{
		Name:        "User",
		Table:       "users",
		PrimaryKeys: []string{"id"},
		Fields:      map[string]*protocol.Field{},
	}
	class.AddField(&protocol.Field{Name: "id", Column: "id", Type: "integer", IsPrimary: true})
	class.AddField(&protocol.Field{Name: "name", Column: "name", Type: "text"})
	if my.governed {
		class.Permissions = map[string]*protocol.Permission{
			"admin": {Select: []string{"id", "name"}},
		}
	}
	return host.PutNode(class.Name, class)
}

type retainedBindingLoader struct{ resolver string }

func (retainedBindingLoader) Name() string  { return "binding" }
func (retainedBindingLoader) Support() bool { return true }
func (retainedBindingLoader) Priority() int { return 1 }
func (my retainedBindingLoader) Load(host protocol.Hoster) error {
	class := &protocol.Class{Name: "User", Table: "users", Fields: map[string]*protocol.Field{}, Permissions: map[string]*protocol.Permission{"admin": {Select: []string{"id"}}}}
	class.AddField(&protocol.Field{Name: "id", Column: "id", Type: "integer", IsPrimary: true, Resolver: my.resolver})
	return host.PutNode(class.Name, class)
}

type rolePolicyLoader string

func (rolePolicyLoader) Name() string  { return "role-policy" }
func (rolePolicyLoader) Support() bool { return true }
func (rolePolicyLoader) Priority() int { return 1 }
func (my rolePolicyLoader) Load(host protocol.Hoster) error {
	class := &protocol.Class{Name: "User", Table: "users", Fields: map[string]*protocol.Field{}, Permissions: map[string]*protocol.Permission{string(my): {Select: []string{"id"}}}}
	class.AddField(&protocol.Field{Name: "id", Column: "id", Type: "integer", IsPrimary: true})
	return host.PutNode(class.Name, class)
}

func snapshotKonfig(t *testing.T) *std.Konfig {
	t.Helper()
	k, err := std.NewKonfig()
	require.NoError(t, err)
	k.Set("mode", "dev")
	k.Set("schema.introspection", true)
	k.Set("app.root", t.TempDir())
	return k
}

func snapshotOptions(loader protocol.Loader) []MetadataOption {
	return []MetadataOption{
		WithoutLoader("pgsql", "mysql", "file", "config"),
		WithLoader(loader),
	}
}

func TestBuilderRequiresPolicyOrMetadataPermissions(t *testing.T) {
	_, err := New(snapshotKonfig(t), nil, snapshotOptions(snapshotLoader{})...).Build()
	require.ErrorContains(t, err, "policy")

	engine, err := New(snapshotKonfig(t), nil, snapshotOptions(snapshotLoader{})...).testAllowAllPolicy().Build()
	require.NoError(t, err)
	require.NotNil(t, engine)

	engine, err = New(snapshotKonfig(t), nil, snapshotOptions(snapshotLoader{governed: true})...).Build()
	require.NoError(t, err)
	require.NotNil(t, engine)
}

func TestBuilderBuildsFrozenConformantSnapshot(t *testing.T) {
	resolver := NewResolver[Root, struct{}, string]("Query", "greeting", "问候",
		func(context.Context, Root, struct{}) (string, error) { return "hello", nil }, Access("test", "read"),
	)
	builder := New(snapshotKonfig(t), nil, snapshotOptions(snapshotLoader{})...).testAllowAllPolicy()
	require.NoError(t, builder.Register(resolver))
	engine, err := builder.Build()
	require.NoError(t, err)

	snapshot, err := engine.Snapshot()
	require.NoError(t, err)
	require.NotEmpty(t, snapshot.Fingerprint())
	require.Contains(t, snapshot.Schema(), "greeting: String")
	capability, ok := snapshot.Capability("Query.greeting")
	require.True(t, ok)
	require.Equal(t, HandlerResolver, capability.Handler)
	require.Equal(t, "Query.greeting", capability.Coordinate)
	upsert, ok := snapshot.Capability("Mutation.upsertUsers")
	require.True(t, ok)
	require.Equal(t, []string{protocol.OP_INSERT, protocol.OP_UPDATE}, upsert.Actions)

	capabilities := snapshot.Capabilities()
	require.NotEmpty(t, capabilities)
	capabilities[0].Coordinate = "mutated"
	require.NotEqual(t, "mutated", snapshot.Capabilities()[0].Coordinate)
	require.ErrorContains(t, builder.Register(resolver), "冻结")

	secondBuilder := New(snapshotKonfig(t), nil, snapshotOptions(snapshotLoader{})...).testAllowAllPolicy()
	require.NoError(t, secondBuilder.Register(resolver))
	second, err := secondBuilder.Build()
	require.NoError(t, err)
	secondSnapshot, err := second.Snapshot()
	require.NoError(t, err)
	require.Equal(t, snapshot.Fingerprint(), secondSnapshot.Fingerprint(), "时间戳不得污染结构指纹")
}

func TestSnapshotFingerprintIncludesGovernanceContract(t *testing.T) {
	build := func(role string) *Snapshot {
		engine, err := New(snapshotKonfig(t), nil, snapshotOptions(rolePolicyLoader(role))...).Build()
		require.NoError(t, err)
		snapshot, err := engine.Snapshot()
		require.NoError(t, err)
		return snapshot
	}
	require.Equal(t, build("admin").Fingerprint(), build("viewer").Fingerprint())
}

func TestSnapshotRejectsMetadataBindingWithoutImplementation(t *testing.T) {
	_, err := New(snapshotKonfig(t), nil, snapshotOptions(retainedBindingLoader{resolver: "User.id"})...).Build()
	require.ErrorContains(t, err, "未注册resolver")
}

func TestLegacyAndBuilderShareSnapshotFinalization(t *testing.T) {
	resolver := NewResolver[Root, struct{}, string]("Query", "greeting", "问候",
		func(context.Context, Root, struct{}) (string, error) { return "hello", nil }, Access("test", "read"),
	)
	meta, err := NewMetadata(snapshotKonfig(t), nil, snapshotOptions(snapshotLoader{})...)
	require.NoError(t, err)
	compiler, err := NewCompiler(meta, nil)
	require.NoError(t, err)
	legacy, err := newEngineForTest(nil, NewRenderer(meta), meta, compiler)
	require.NoError(t, err)
	require.NoError(t, legacy.register(resolver))
	legacySnapshot, err := legacy.Snapshot()
	require.NoError(t, err)
	// Direct construction is test-only; lifecycle mutation is deliberately private.

	builder := New(snapshotKonfig(t), nil, snapshotOptions(snapshotLoader{})...).testAllowAllPolicy()
	require.NoError(t, builder.Register(resolver))
	engine, err := builder.Build()
	require.NoError(t, err)
	engineSnapshot, err := engine.Snapshot()
	require.NoError(t, err)
	require.Equal(t, legacySnapshot.Fingerprint(), engineSnapshot.Fingerprint())
}

func TestBuilderSupportsReplaceAndWrapBeforeFreeze(t *testing.T) {
	replacement := NewResolver[Root, struct{}, map[string]interface{}](
		"Query", "users", "replace generated query",
		func(context.Context, Root, struct{}) (map[string]interface{}, error) {
			return map[string]interface{}{"items": []interface{}{}, "total": 0}, nil
		}, Result("UserResult!"), Access("test", "read"),
	)
	builder := New(snapshotKonfig(t), nil, snapshotOptions(snapshotLoader{})...).testAllowAllPolicy()
	require.NoError(t, builder.Replace(replacement))
	require.NoError(t, builder.Wrap("Query.users", NewResolverMiddleware[
		Root, struct{}, map[string]interface{},
	](func(ctx context.Context, source Root, input struct{}, next NextResolver[Root, struct{}, map[string]interface{}]) (map[string]interface{}, error) {
		return next(ctx, source, input)
	})))

	engine, err := builder.Build()
	require.NoError(t, err)
	snapshot, err := engine.Snapshot()
	require.NoError(t, err)
	capability, ok := snapshot.Capability("Query.users")
	require.True(t, ok)
	require.Equal(t, HandlerResolver, capability.Handler)
	require.NotEmpty(t, capability.Arguments)
	capability.Arguments[0].Name = "mutated"
	unchanged, ok := snapshot.Capability("Query.users")
	require.True(t, ok)
	require.NotEqual(t, "mutated", unchanged.Arguments[0].Name)
	require.ErrorContains(t, builder.Replace(replacement), "冻结")
	require.ErrorContains(t, builder.Wrap("Query.users", nil), "冻结")
}

func TestBuilderLoadsDocumentsBeforeFreeze(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "greeting.graphql"), []byte("query Greeting { greeting }"), 0o600))
	resolver := NewResolver[Root, struct{}, string]("Query", "greeting", "问候",
		func(context.Context, Root, struct{}) (string, error) { return "hello", nil }, Access("test", "read"),
	)
	builder := New(snapshotKonfig(t), nil, snapshotOptions(snapshotLoader{})...).testAllowAllPolicy()
	require.NoError(t, builder.Register(resolver))
	require.NoError(t, builder.LoadDocuments(dir))
	engine, err := builder.Build()
	require.NoError(t, err)
	require.Contains(t, engine.documents, "Greeting")
	require.ErrorContains(t, builder.LoadDocuments(dir), "冻结")
}

func TestBuilderRejectsSchemaFieldWithoutHandler(t *testing.T) {
	k := snapshotKonfig(t)
	path := filepath.Join(t.TempDir(), "schema.graphql")
	require.NoError(t, os.WriteFile(path, []byte("type Query { ghost: String }"), 0o600))
	k.Set("schema.file", path)

	_, err := New(k, nil, snapshotOptions(snapshotLoader{})...).testAllowAllPolicy().Build()
	require.ErrorContains(t, err, "schema artifact")
}

func TestBuilderRejectsExternalSchemaShapeDrift(t *testing.T) {
	k := snapshotKonfig(t)
	path := filepath.Join(t.TempDir(), "schema.graphql")
	// Coordinate remains recognizable, but the SQL contract no longer matches the catalog.
	require.NoError(t, os.WriteFile(path, []byte("type Query { users(limit: String): UserResult! }\n"+
		"type UserResult { items: [User!]!, total: Int!, ghost: String }\n"+
		"type User { id: ID!, name: String }"), 0o600))
	k.Set("schema.file", path)

	_, err := New(k, nil, snapshotOptions(snapshotLoader{})...).testAllowAllPolicy().Build()
	require.ErrorContains(t, err, "schema artifact")
}

func TestBuilderRejectsExecutableDirectiveWithoutRuntime(t *testing.T) {
	builder := New(snapshotKonfig(t), nil, snapshotOptions(snapshotLoader{})...).testAllowAllPolicy()
	require.NoError(t, builder.Register(NewResolver[Root, struct{}, string]("Query", "hello", "",
		func(context.Context, Root, struct{}) (string, error) { return "hello", nil },
		Extra("directive @upper on FIELD"))))
	_, err := builder.Build()
	require.ErrorContains(t, err, "unsupported executable directive @upper")
}

func TestRemoteSnapshotRejectsExecutableDirectiveWithoutRuntime(t *testing.T) {
	_, err := NewSnapshotArtifact("directive @upper on FIELD\ntype Query { hello: String }", "revision", nil)
	require.ErrorContains(t, err, "unsupported executable directive @upper")
}

func TestRemoteSnapshotDerivesCompleteCapabilityCatalog(t *testing.T) {
	schema := "type Query { hello(name: String): String }"
	capabilities := []Capability{{Coordinate: "Query.hello", Parent: "Query", Name: "hello", Type: "String", Arguments: []Argument{{Name: "name", Type: "String"}}, Handler: HandlerResolver}}
	fingerprint, err := ArtifactFingerprint(schema, capabilities)
	require.NoError(t, err)
	snapshot, err := NewSnapshotArtifact(schema, fingerprint, capabilities)
	require.NoError(t, err)
	capability, ok := snapshot.Capability("Query.hello")
	require.True(t, ok)
	require.Equal(t, HandlerResolver, capability.Handler)
	require.Equal(t, "String", capability.Type)
	require.Equal(t, []Argument{{Name: "name", Type: "String"}}, capability.Arguments)

	schema = "type Query { hello: String }"
	capabilities = []Capability{{
		Coordinate: "Query.missing", Parent: "Query", Name: "missing", Type: "String", Handler: HandlerResolver,
	}}
	fingerprint, err = ArtifactFingerprint(schema, capabilities)
	require.NoError(t, err)
	_, err = NewSnapshotArtifact(schema, fingerprint, capabilities)
	require.ErrorContains(t, err, "executable root capability missing")
}

func TestRemoteSnapshotDoesNotMutateCallerCapabilities(t *testing.T) {
	capabilities := append(make([]Capability, 0, 256),
		Capability{Coordinate: "Query.second", Parent: "Query", Name: "second", Type: "String", Handler: HandlerResolver},
		Capability{Coordinate: "Query.first", Parent: "Query", Name: "first", Type: "String", Handler: HandlerResolver},
	)
	schema := "type Query { first: String, second: String }"
	fingerprint, err := ArtifactFingerprint(schema, capabilities)
	require.NoError(t, err)
	_, err = NewSnapshotArtifact(schema, fingerprint, capabilities)
	require.NoError(t, err)
	require.Equal(t, "Query.second", capabilities[0].Coordinate)
}

func TestBuilderRenderingHasNoImplicitFileWrite(t *testing.T) {
	root := t.TempDir()
	k := snapshotKonfig(t)
	k.Set("app.root", root)
	_, err := New(k, nil, snapshotOptions(snapshotLoader{})...).testAllowAllPolicy().Build()
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(root, "cfg", "schema.graphql"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
