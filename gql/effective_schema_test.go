package gql

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

type effectiveCapabilityEndpoint struct{ *gatewayEndpoint }

func (effectiveCapabilityEndpoint) EffectiveSchemaAvailable() bool { return true }

func TestCheckProfileCompatibilityReportsNullableDefaultChanges(t *testing.T) {
	previous := SchemaProfile{Name: "previous", SchemaArtifact: SchemaArtifact{Schema: `directive @d(arg: String = "old") on FIELD_DEFINITION
type Query { user(arg: String = "old"): String }
input Input { value: String = "old" }`}}
	next := SchemaProfile{Name: "next", SchemaArtifact: SchemaArtifact{Schema: `directive @d(arg: String = "new") on FIELD_DEFINITION
type Query { user(arg: String = "new"): String }
input Input { value: String = "new" }`}}
	err := CheckProfileCompatibility(previous, next)
	compat, ok := err.(SchemaCompatibilityError)
	require.True(t, ok)
	require.Equal(t, []string{"@d(arg)", "Input.value", "Query.user(arg)"}, []string{compat.Violations[0].Coordinate, compat.Violations[1].Coordinate, compat.Violations[2].Coordinate})
}

func TestCheckProfileCompatibilityReportsPossibleTypeRemoval(t *testing.T) {
	previous := SchemaProfile{Name: "previous", SchemaArtifact: SchemaArtifact{Schema: `interface Node { id: ID! }
type User implements Node { id: ID! }
type Team implements Node { id: ID! }
union Search = User | Team
type Query { node: Node, search: Search }`}}
	next := SchemaProfile{Name: "next", SchemaArtifact: SchemaArtifact{Schema: `interface Node { id: ID! }
type User implements Node { id: ID! }
union Search = User
type Query { node: Node, search: Search }`}}
	err := CheckProfileCompatibility(previous, next)
	compat, ok := err.(SchemaCompatibilityError)
	require.True(t, ok)
	require.Contains(t, compat.Violations, SchemaCompatibilityViolation{Category: "union_member_removed", Coordinate: "Search.Team", Message: "removed union member Search.Team"})
	// Removing an object from an interface's possible types is also breaking.
	next.Schema = strings.Replace(next.Schema, "type User implements Node", "type User", 1)
	err = CheckProfileCompatibility(previous, next)
	compat, ok = err.(SchemaCompatibilityError)
	require.True(t, ok)
	require.Contains(t, compat.Violations, SchemaCompatibilityViolation{Category: "interface_implementation_removed", Coordinate: "User implements Node", Message: "removed interface implementation User implements Node"})
}

type blockingCachedPolicy struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (p *blockingCachedPolicy) Decide(ctx context.Context, request PolicyRequest) (PolicyDecision, error) {
	p.calls.Add(1)
	p.once.Do(func() {
		close(p.started)
		select {
		case <-p.release:
		case <-ctx.Done():
		}
	})
	if err := ctx.Err(); err != nil {
		return PolicyDecision{}, err
	}
	return PolicyDecision{Allow: true, Fields: map[FieldUse][]string{
		FieldUseSelect: {"*"}, FieldUseFilter: {"*"}, FieldUseOrder: {"*"}, FieldUseGroup: {"*"},
		FieldUseAggregate: {"*"}, FieldUseJoin: {"*"}, FieldUseWrite: {"*"}, FieldUseReturn: {"*"},
		FieldUseInternalDependency: {"*"},
	}}, nil
}

func (*blockingCachedPolicy) PolicyCacheIdentity(context.Context, Identity, TenantRoute, map[string]any) (string, bool) {
	return "stable", true
}

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

func TestIntersectFieldShapePreservesUsesMissingFromNextDecision(t *testing.T) {
	current := map[FieldUse][]string{
		FieldUseSelect: {"id", "name"},
		FieldUseReturn: {"id", "name"},
	}
	intersectFieldShape(current, map[FieldUse][]string{FieldUseReturn: {"name"}})
	require.Equal(t, []string{"id", "name"}, current[FieldUseSelect])
	require.Equal(t, []string{"name"}, current[FieldUseReturn])
}

func TestEffectiveSchemaIntersectsOutputAcrossSelectAndReturn(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	installTestPolicy(t, executor, OperationPolicyProvider(func(_ context.Context, request PolicyRequest) (PolicyDecision, error) {
		fields := request.Fields
		switch request.Coordinate {
		case "Query.users":
			fields = map[FieldUse][]string{FieldUseSelect: {"id", "name"}}
		case "Mutation.createUser":
			fields = map[FieldUse][]string{FieldUseWrite: {"name", "email"}, FieldUseReturn: {"id"}}
		}
		return PolicyDecision{Allow: true, Fields: fields}, nil
	}))
	identity, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "u"})
	require.NoError(t, err)
	artifact, err := executor.EffectiveSchema(WithIdentity(context.Background(), identity))
	require.NoError(t, err)
	schema, gqlErr := gqlparser.LoadSchema(&ast.Source{Name: "effective.graphql", Input: artifact.Schema})
	require.Empty(t, gqlErr)
	require.NotNil(t, schema.Types["User"].Fields.ForName("id"))
	require.Nil(t, schema.Types["User"].Fields.ForName("name"))
}

func TestEffectiveSchemaRemovesDeniedNestedResolver(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	require.NoError(t, executor.register(NewResolver[Source, struct{}, string]("User", "secret", "", func(context.Context, Source, struct{}) (string, error) {
		return "hidden", nil
	}, Access("User", protocol.OP_SELECT))))
	installTestPolicy(t, executor, OperationPolicyProvider(func(_ context.Context, request PolicyRequest) (PolicyDecision, error) {
		return PolicyDecision{Allow: request.Coordinate != "User.secret", Fields: request.Fields}, nil
	}))
	identity, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "u"})
	require.NoError(t, err)
	artifact, err := executor.EffectiveSchema(WithIdentity(context.Background(), identity))
	require.NoError(t, err)
	require.NotContains(t, artifact.Schema, "secret:")
}

func TestEffectiveSchemaDoesNotExposeCursorsForHiddenOrderingValues(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	installTestPolicy(t, executor, OperationPolicyProvider(func(_ context.Context, request PolicyRequest) (PolicyDecision, error) {
		fields := request.Fields
		if request.Coordinate == "Query.users" {
			fields = map[FieldUse][]string{FieldUseSelect: {"name"}, FieldUseOrder: {"email"}}
		}
		return PolicyDecision{Allow: true, Fields: fields}, nil
	}))
	identity, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "u"})
	require.NoError(t, err)
	artifact, err := executor.EffectiveSchema(WithIdentity(context.Background(), identity))
	require.NoError(t, err)
	schema, gqlErr := gqlparser.LoadSchema(&ast.Source{Name: "effective.graphql", Input: artifact.Schema})
	require.Empty(t, gqlErr)
	users := schema.Query.Fields.ForName("users")
	require.Nil(t, users.Arguments.ForName(protocol.FIRST), "cursor tuple includes the hidden primary key")
	require.Nil(t, schema.Query.Fields.ForName("userConnection"), "connections always emit a cursor containing the primary key")
	if sortInput := schema.Types["UserSortInput"]; sortInput != nil {
		require.Nil(t, sortInput.Fields.ForName("email"), "cursor sorting must not expose a hidden field value")
	}
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

func TestEffectiveSchemaFollowerSurvivesLeaderCancellation(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	provider := &blockingCachedPolicy{started: make(chan struct{}), release: make(chan struct{})}
	installTestPolicy(t, executor, provider)
	identity, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "u", PolicyRevision: 1})
	require.NoError(t, err)
	ctx := WithIdentity(context.Background(), identity)
	leaderCtx, cancel := context.WithCancel(ctx)
	leader := make(chan error, 1)
	go func() { _, err := executor.EffectiveSchema(leaderCtx); leader <- err }()
	<-provider.started
	follower := make(chan error, 1)
	go func() { _, err := executor.EffectiveSchema(ctx); follower <- err }()
	require.Eventually(t, func() bool { return provider.calls.Load() == 1 }, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-leader, context.Canceled)
	select {
	case err := <-follower:
		require.Failf(t, "healthy follower inherited leader cancellation", "error: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(provider.release)
	require.NoError(t, <-follower)
}

func TestEffectiveSchemaRemovesUnauthorizedRelationOperations(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	installTestPolicy(t, executor, OperationPolicyProvider(func(_ context.Context, request PolicyRequest) (PolicyDecision, error) {
		if request.Resource == "Post" && operationInList(request.Actions, protocol.OP_UPDATE) {
			return PolicyDecision{Allow: false}, nil
		}
		return PolicyDecision{Allow: true, Fields: map[FieldUse][]string{
			FieldUseSelect: {"*"}, FieldUseReturn: {"*"}, FieldUseFilter: {"*"}, FieldUseOrder: {"*"},
			FieldUseGroup: {"*"}, FieldUseAggregate: {"*"}, FieldUseJoin: {"*"}, FieldUseWrite: {"*"},
			FieldUseInternalDependency: {"*"},
		}}, nil
	}))
	identity, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "u"})
	require.NoError(t, err)
	artifact, err := executor.EffectiveSchema(WithIdentity(context.Background(), identity))
	require.NoError(t, err)
	schema, gqlErr := gqlparser.LoadSchema(&ast.Source{Name: "effective.graphql", Input: artifact.Schema})
	require.Empty(t, gqlErr)
	relation := schema.Types["PostCreateRelationInput"]
	require.NotNil(t, relation)
	require.Nil(t, relation.Fields.ForName("connect"))
	require.NotNil(t, relation.Fields.ForName("create"))
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

func TestSchemaProfileCompatibilityDetectsAbstractRootsAndDirectiveBreaks(t *testing.T) {
	previous := SchemaProfile{Name: "v1", SchemaArtifact: SchemaArtifact{Schema: `
		directive @tag(label: String!) repeatable on FIELD_DEFINITION | OBJECT
		schema { query: Root mutation: Write }
		type Root { search: Result @tag(label: "x") }
		type Write { save: String }
		interface Node { id: ID! }
		type User implements Node { id: ID! }
		union Result = User
	`}}
	next := SchemaProfile{Name: "v2", SchemaArtifact: SchemaArtifact{Schema: `
		directive @tag(label: String) on FIELD_DEFINITION
		schema { query: Root }
		type Root { search: Result @tag(label: "x") }
		type Write { save: String }
		interface Node { id: ID! }
		type User { id: ID! }
		union Result = Root
	`}}
	err := CheckProfileCompatibility(previous, next)
	require.Error(t, err)
	for _, want := range []string{"category=", "coordinate=", "union", "interface", "schema", "directive"} {
		require.Contains(t, err.Error(), want)
	}
}

func TestSchemaProfileCompatibilityReportsDirectiveParameterAndLocationCoordinates(t *testing.T) {
	previous := SchemaProfile{Name: "v1", SchemaArtifact: SchemaArtifact{Schema: `
		directive @auth(role: String! = "reader") on FIELD_DEFINITION | OBJECT
		type Query { value: String @auth }
	`}}
	next := SchemaProfile{Name: "v2", SchemaArtifact: SchemaArtifact{Schema: `
		directive @auth(role: Int) on FIELD_DEFINITION
		type Query { value: String @auth }
	`}}
	err := CheckProfileCompatibility(previous, next)
	require.Error(t, err)
	require.Contains(t, err.Error(), "coordinate=@auth(role)")
}

func TestSchemaProfileCompatibilityAllowsAdditiveRootsAndDirectiveCapabilities(t *testing.T) {
	previous := SchemaProfile{SchemaArtifact: SchemaArtifact{Schema: `
		directive @tag on FIELD_DEFINITION
		type Query { value: String }
	`}}
	next := SchemaProfile{SchemaArtifact: SchemaArtifact{Schema: `
		directive @tag repeatable on FIELD_DEFINITION | OBJECT
		type Query { value: String }
		type Mutation { save: String }
	`}}
	require.NoError(t, CheckProfileCompatibility(previous, next))
}

func TestSchemaArtifactExecutionPinBindsCapabilities(t *testing.T) {
	artifact := FinalizeSchemaArtifact(SchemaArtifact{
		Schema: "type Query { value: String }", Fingerprint: digest("type Query { value: String }"),
		Capabilities: []Capability{{Coordinate: "Query.value", Parent: "Query", Name: "value", Type: "String", Handler: HandlerSQL, RetrySafe: true}},
	})
	require.NoError(t, ValidateSchemaArtifact(artifact))
	artifact.Capabilities[0].RetrySafe = false
	require.ErrorContains(t, ValidateSchemaArtifact(artifact), "execution pin mismatch")
}

func TestExecutionPinIsEnforcedAtEffectiveViewSelection(t *testing.T) {
	artifact := SchemaArtifact{Schema: "type Query { value: String }", Fingerprint: digest("type Query { value: String }")}
	pin := FinalizeSchemaArtifact(artifact).ExecutionPin
	require.NoError(t, enforceExecutionPin(WithExecutionPin(context.Background(), pin), artifact))
	err := enforceExecutionPin(WithExecutionPin(context.Background(), "stale"), artifact)
	require.Equal(t, "SNAPSHOT_CONFLICT", err.(errorCoder).ErrorCode())
}
