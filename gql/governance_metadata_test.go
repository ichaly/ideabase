package gql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCapabilityDescribesNestedRelationWriteRefresh(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	snapshot, err := executor.Snapshot()
	require.NoError(t, err)
	create, ok := snapshot.Capability("Mutation.createPost")
	require.True(t, ok)
	require.True(t, create.RefreshRequired)
	require.Equal(t, SemanticCommand, create.SemanticKind)
	query, ok := snapshot.Capability("Query.posts")
	require.True(t, ok)
	require.Equal(t, SemanticPrimitive, query.SemanticKind)
	projection, ok := snapshot.Capability("Post.tags")
	require.True(t, ok)
	require.Equal(t, SemanticProjection, projection.SemanticKind)
}

func TestCustomHandlerSemanticKindIsDiscoverable(t *testing.T) {
	define := Define{Class: "Mutation", Name: "run", SemanticKind: SemanticWorkflow}
	require.Equal(t, SemanticWorkflow, define.SemanticKind)
	define = Define{Class: "Query", Name: "value"}
	require.Equal(t, SemanticPrimitive, deriveSemanticKind(define))
	define = Define{Class: "Mutation", Name: "save"}
	require.Equal(t, SemanticCommand, deriveSemanticKind(define))
	define = Define{Class: "User", Name: "display"}
	require.Equal(t, SemanticProjection, deriveSemanticKind(define))
	define = Define{Class: "Query", Name: "run"}
	Semantic(SemanticWorkflow)(&define)
	require.Equal(t, SemanticWorkflow, deriveSemanticKind(define))
	executor, _, cleanup := setupRootResolverExecutor(t)
	defer cleanup()
	require.NoError(t, executor.register(NewResolver[Root[struct{}, string], struct{}, string]("Query", "workflow", "", func(context.Context, Root[struct{}, string], struct{}) (string, error) {
		return "ok", nil
	}, Semantic(SemanticWorkflow))))
	snapshot, err := executor.Snapshot()
	require.NoError(t, err)
	capability, ok := snapshot.Capability("Query.workflow")
	require.True(t, ok)
	require.Equal(t, SemanticWorkflow, capability.SemanticKind)
}

func TestCustomHandlerRejectsUnknownSemanticKind(t *testing.T) {
	resolver := NewResolver("Query", "badSemantic", "", func(context.Context, Root[struct{}, string], struct{}) (string, error) {
		return "", nil
	}, Semantic("unknown"), Access("test", "read"))
	builder := New(snapshotKonfig(t), nil, snapshotOptions(snapshotLoader{})...).testAllowAllPolicy()
	require.NoError(t, builder.Register(resolver))
	_, err := builder.Build()
	require.ErrorContains(t, err, "semantic kind")
}

func TestSchemaArtifactBindsIdentityDescriptorToExecutionPin(t *testing.T) {
	identity, err := NewIdentity(IdentityInput{Tenant: "tenant-a", Subject: "user-1", Actor: "actor-1", Roles: []string{"admin"}, PolicyRevision: 4, CredentialEpoch: 8})
	require.NoError(t, err)
	artifact := FinalizeSchemaArtifact(SchemaArtifact{
		Schema: "type Query { value: String }", Fingerprint: digest("type Query { value: String }"),
		Identity: IdentityDescriptorFromIdentity(identity),
	})
	require.NotEmpty(t, artifact.ExecutionPin)
	require.NoError(t, ValidateSchemaArtifact(artifact))
	artifact.Identity.Roles = []string{"viewer"}
	require.ErrorContains(t, ValidateSchemaArtifact(artifact), "execution pin mismatch")
}
