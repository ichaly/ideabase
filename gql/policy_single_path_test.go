package gql

import (
	"context"
	"testing"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/require"
)

func TestBuilderPolicyRejectsMetadataPermissionConflict(t *testing.T) {
	provider := OperationPolicyProvider(func(context.Context, PolicyRequest) (PolicyDecision, error) {
		return PolicyDecision{Allow: true}, nil
	})
	builder := New(snapshotKonfig(t), nil, snapshotOptions(snapshotLoader{governed: true})...)
	require.NoError(t, builder.Policy(provider))
	_, err := builder.Build()
	require.ErrorContains(t, err, "metadata permissions")
}

func TestBuilderAdaptsMetadataPermissionsIntoTheRuntimeProvider(t *testing.T) {
	engine, err := New(snapshotKonfig(t), nil, snapshotOptions(snapshotLoader{governed: true})...).Build()
	require.NoError(t, err)
	require.IsType(t, (*ProtocolPolicyProvider)(nil), engine.policy)
	identity, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "u", Roles: []string{"admin"}})
	require.NoError(t, err)
	artifact, err := engine.EffectiveSchema(WithIdentity(context.Background(), identity))
	require.NoError(t, err)
	require.Contains(t, schemaBlock(artifact.Schema, "type User"), "id:")
	require.Contains(t, schemaBlock(artifact.Schema, "type User"), "name:")
}

func TestBuilderPolicyIsTheOnlyProviderSource(t *testing.T) {
	provider := OperationPolicyProvider(func(context.Context, PolicyRequest) (PolicyDecision, error) {
		return PolicyDecision{Allow: true}, nil
	})
	builder := New(snapshotKonfig(t), nil, snapshotOptions(snapshotLoader{})...)
	require.NoError(t, builder.Policy(provider))
	require.ErrorContains(t, builder.Register(provider), "PolicyProvider")
	engine, err := builder.Build()
	require.NoError(t, err)
	require.IsType(t, provider, engine.policy)
}

func TestEngineAssemblyRejectsPolicyProviderRegistration(t *testing.T) {
	meta := emptyMetadata(t)
	compiler, err := NewCompiler(meta, nil)
	require.NoError(t, err)
	executor, err := newEngineForTest(nil, NewRenderer(meta), meta, compiler)
	require.NoError(t, err)
	provider := NewProtocolPolicyProvider(map[string]map[string]*protocol.Permission{})
	require.ErrorContains(t, executor.register(provider), "Builder.Policy")
}

// installTestPolicy models the pre-Build injection used by direct Engine
// unit tests. Production callers must use Builder.Policy.
func installTestPolicy(t *testing.T, executor *Engine, provider PolicyProvider) {
	t.Helper()
	require.NotNil(t, provider)
	require.Nil(t, executor.policy)
	executor.policy = provider
}
