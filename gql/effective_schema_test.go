package gql

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEffectiveSchemaCacheIncludesScopeValues(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	installTestPolicy(t, executor, OperationPolicyProvider(func(_ context.Context, request PolicyRequest) (PolicyDecision, error) {
		if request.Coordinate != "Query.users" {
			return PolicyDecision{Allow: true, Fields: request.Fields, PolicyRevision: request.Identity.PolicyRevision()}, nil
		}
		fields := []string{"id"}
		if request.Values["variant"] == "wide" {
			fields = append(fields, "name")
		}
		return PolicyDecision{Allow: true, Fields: map[FieldUse][]string{FieldUseSelect: fields}, PolicyRevision: request.Identity.PolicyRevision()}, nil
	}))
	identity, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "u", PolicyRevision: 1})
	require.NoError(t, err)
	narrow, err := executor.EffectiveSchema(WithScope(WithIdentity(context.Background(), identity), map[string]any{"variant": "narrow"}))
	require.NoError(t, err)
	wide, err := executor.EffectiveSchema(WithScope(WithIdentity(context.Background(), identity), map[string]any{"variant": "wide"}))
	require.NoError(t, err)
	require.NotEqual(t, narrow.Fingerprint, wide.Fingerprint)
	require.True(t, strings.Contains(wide.Schema, "name"))
}

func TestEffectiveSchemaCacheIsBoundedLRU(t *testing.T) {
	cache := newEffectiveCache(2)
	a := &effectiveView{}
	b := &effectiveView{}
	c := &effectiveView{}
	cache.Put("a", a)
	cache.Put("b", b)
	_, ok := cache.Get("a")
	require.True(t, ok)
	cache.Put("c", c)
	_, ok = cache.Get("b")
	require.False(t, ok, "the least recently used schema should be evicted")
	_, ok = cache.Get("a")
	require.True(t, ok)
}

func TestDynamicPolicyProviderWithoutCacheIdentityIsNotCached(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	calls := 0
	installTestPolicy(t, executor, OperationPolicyProvider(func(_ context.Context, request PolicyRequest) (PolicyDecision, error) {
		calls++
		return PolicyDecision{Allow: true, Fields: request.Fields}, nil
	}))
	identity, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "u", PolicyRevision: 1})
	require.NoError(t, err)
	ctx := WithIdentity(context.Background(), identity)
	_, err = executor.EffectiveSchema(ctx)
	require.NoError(t, err)
	_, err = executor.EffectiveSchema(ctx)
	require.NoError(t, err)
	require.Greater(t, calls, 5, "dynamic providers must be evaluated again without an explicit cache identity")
}

func TestSchemaProfileCompatibilityDetectsClientBreakingChanges(t *testing.T) {
	previous := SchemaProfile{Name: "v1", SchemaArtifact: SchemaArtifact{Schema: `
		type Query { users(limit: Int): [User!]! }
		type User { id: ID!, role: Role! }
		input Filter { name: String }
		enum Role { ADMIN USER }
	`}}
	tests := map[string]string{
		"removed enum value":       `type Query { users(limit: Int): [User!]! } type User { id: ID!, role: Role! } enum Role { USER }`,
		"removed argument":         `type Query { users: [User!]! } type User { id: ID!, role: Role! } enum Role { ADMIN USER }`,
		"changed argument":         `type Query { users(limit: Float): [User!]! } type User { id: ID!, role: Role! } enum Role { ADMIN USER }`,
		"new required argument":    `type Query { users(limit: Int, tenant: ID!): [User!]! } type User { id: ID!, role: Role! } enum Role { ADMIN USER }`,
		"new required input field": `type Query { users(limit: Int, filter: Filter): [User!]! } type User { id: ID!, role: Role! } input Filter { name: String, tenant: ID! } enum Role { ADMIN USER }`,
	}
	for name, schema := range tests {
		t.Run(name, func(t *testing.T) {
			err := CheckProfileCompatibility(previous, SchemaProfile{Name: "v2", SchemaArtifact: SchemaArtifact{Schema: schema}})
			require.ErrorContains(t, err, "incompatible schema profile")
		})
	}
}

func TestSchemaProfileCompatibilityDetectsRequiredDefaultsAndKindChanges(t *testing.T) {
	for name, pair := range map[string][2]string{
		"argument default removed": {
			`type Query { users(limit: Int! = 10): [String!]! }`,
			`type Query { users(limit: Int!): [String!]! }`,
		},
		"input default removed": {
			`type Query { users(filter: Filter): [String!]! } input Filter { tenant: ID! = "t" }`,
			`type Query { users(filter: Filter): [String!]! } input Filter { tenant: ID! }`,
		},
		"named type kind changed": {
			`scalar Token type Query { token: Token }`,
			`enum Token { VALUE } type Query { token: Token }`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := CheckProfileCompatibility(
				SchemaProfile{Name: "v1", SchemaArtifact: SchemaArtifact{Schema: pair[0]}},
				SchemaProfile{Name: "v2", SchemaArtifact: SchemaArtifact{Schema: pair[1]}},
			)
			require.ErrorContains(t, err, "incompatible schema profile")
		})
	}
}
