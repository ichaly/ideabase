package gql

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

const gatewayChildSchema = `
scalar Json
type Query { user: User!, users: [User!]! }
type Mutation { setName(input: UserInput!): User }
type User { id: ID!, name: String, status: Status, payload: Json }
input UserInput { name: String! }
enum Status { ACTIVE DISABLED }
`

const gatewaySubscriptionSchema = gatewayChildSchema + `
type Subscription { changed: User!, removed: User }
`

const gatewayAbstractSchema = `
interface Node { id: ID! }
type User implements Node { id: ID!, name: String! }
type Team implements Node { id: ID!, slug: String! }
union SearchResult = User | Team
type Query { node: Node!, search: [SearchResult!]! }
`

type gatewayEndpoint struct {
	id       string
	snapshot *Snapshot
	execute  func(context.Context, string, map[string]any) ([]byte, error)
	calls    atomic.Int32
}

type gatewaySubscriptionEndpoint struct {
	*gatewayEndpoint
	events <-chan []byte
}

type gatewayHealthyEndpoint struct {
	*gatewayEndpoint
	report *HealthReport
}

type concurrentHealthEndpoint struct {
	*gatewayEndpoint
	entered *atomic.Int32
	ready   chan struct{}
	once    *sync.Once
}

type gatewayEffectiveEndpoint struct {
	*gatewayEndpoint
	effective   SchemaArtifact
	effectiveFn func(context.Context) (SchemaArtifact, error)
}

type gatewayEffectiveSubscriptionEndpoint struct {
	*gatewaySubscriptionEndpoint
	effective SchemaArtifact
}

type gatewayExplicitStaticEndpoint struct{ *gatewayEndpoint }
type gatewayRawEffectiveEndpoint struct {
	*gatewayEndpoint
	artifact SchemaArtifact
}

type healthDeclaredWithoutEndpoint struct{ *gatewayEndpoint }

func (healthDeclaredWithoutEndpoint) HealthAvailable() bool { return true }

type gatewayCacheIdentityEndpoint struct {
	*gatewayEffectiveEndpoint
	cacheKey       string
	effectiveCalls atomic.Int32
}

func (my *gatewayCacheIdentityEndpoint) EffectiveSchemaCacheIdentity(context.Context) (string, bool) {
	return my.cacheKey, my.cacheKey != ""
}

func (my *gatewayCacheIdentityEndpoint) EffectiveSchema(ctx context.Context) (SchemaArtifact, error) {
	my.effectiveCalls.Add(1)
	return my.gatewayEffectiveEndpoint.EffectiveSchema(ctx)
}

func (gatewayExplicitStaticEndpoint) EffectiveSchemaAvailable() bool { return false }

func (my *gatewayEffectiveEndpoint) EffectiveSchema(ctx context.Context) (SchemaArtifact, error) {
	if my.effectiveFn != nil {
		artifact, err := my.effectiveFn(ctx)
		return FinalizeSchemaArtifact(artifact), err
	}
	return FinalizeSchemaArtifact(my.effective), nil
}

func (my *gatewayEffectiveSubscriptionEndpoint) EffectiveSchema(context.Context) (SchemaArtifact, error) {
	return FinalizeSchemaArtifact(my.effective), nil
}

func (my *gatewayRawEffectiveEndpoint) EffectiveSchema(context.Context) (SchemaArtifact, error) {
	return my.artifact, nil
}

func (my *gatewayHealthyEndpoint) Health(context.Context) (HealthReport, error) {
	if my.report != nil {
		return *my.report, nil
	}
	return HealthReport{Ready: true, SnapshotOK: true, DatabaseOK: true, SnapshotFingerprint: my.snapshot.Fingerprint()}, nil
}

func (my *concurrentHealthEndpoint) Health(ctx context.Context) (HealthReport, error) {
	if my.entered.Add(1) == 2 {
		my.once.Do(func() { close(my.ready) })
	}
	select {
	case <-my.ready:
		return HealthReport{Ready: true, SnapshotOK: true, DatabaseOK: true}, nil
	case <-ctx.Done():
		return HealthReport{}, ctx.Err()
	}
}

func TestGatewayHealthChecksChildrenConcurrently(t *testing.T) {
	var entered atomic.Int32
	ready := make(chan struct{})
	once := new(sync.Once)
	first := &concurrentHealthEndpoint{gatewayEndpoint: newGatewayEndpoint("first", gatewayChildSchema, nil), entered: &entered, ready: ready, once: once}
	second := &concurrentHealthEndpoint{gatewayEndpoint: newGatewayEndpoint("second", gatewayChildSchema, nil), entered: &entered, ready: ready, once: once}
	gateway, err := NewGateway("root", Namespace{Name: "first", Endpoint: first}, Namespace{Name: "second", Endpoint: second})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	report, err := gateway.Health(ctx)
	require.NoError(t, err)
	require.True(t, report.Ready)
}

func TestGatewayPublishesControlAndManifestViews(t *testing.T) {
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: newGatewayEndpoint("crm", gatewayChildSchema, nil)})
	require.NoError(t, err)
	require.Equal(t, "root", gateway.EndpointID())
	require.False(t, gateway.EffectiveSchemaAvailable())
	require.Equal(t, uint64(0), gateway.ManifestRevision())
	state := gateway.ControlState()
	require.NotEmpty(t, state.Fingerprint)
	snapshot, revision, err := gateway.ManifestState()
	require.NoError(t, err)
	require.Equal(t, uint64(0), revision)
	require.Equal(t, state.Fingerprint, snapshot.Fingerprint())
	_, err = gateway.ForSnapshot("missing")
	require.Error(t, err)
}

func (my *gatewaySubscriptionEndpoint) Subscribe(context.Context, string, map[string]any, string) (<-chan []byte, error) {
	return my.events, nil
}

func TestGatewayTranslatesVariableTypesTypenamesAndPreservesNumbers(t *testing.T) {
	child := newGatewayEndpoint("crm-service", gatewayChildSchema, func(_ context.Context, query string, variables map[string]any) ([]byte, error) {
		require.Contains(t, query, "$input: UserInput!")
		require.NotContains(t, query, "Crm_UserInput")
		require.Equal(t, map[string]any{"name": "new"}, variables["input"])
		return []byte(`{"data":{"saved":{"__typename":"User","id":"1","payload":9007199254740993}}}`), nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), `mutation($input: Crm_UserInput!) {
		saved: crm_setName(input: $input) { __typename id payload }
	}`, map[string]any{"input": map[string]any{"name": "new"}})
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"saved":{"__typename":"Crm_User","id":"1","payload":9007199254740993}}}`, string(body))
}

func TestGatewayHealthRequiresEveryExecutableChild(t *testing.T) {
	healthy := &gatewayHealthyEndpoint{gatewayEndpoint: newGatewayEndpoint("healthy", gatewayChildSchema, nil)}
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: healthy})
	require.NoError(t, err)
	require.True(t, gateway.HealthAvailable())
	report, err := gateway.Health(context.Background())
	require.NoError(t, err)
	require.True(t, report.Ready)
	require.Equal(t, gateway.current().snapshot.Fingerprint(), report.SnapshotFingerprint)

	unhealthy := newGatewayEndpoint("unknown-health", gatewayChildSchema, nil)
	gateway, err = NewGateway("root", Namespace{Name: "crm", Endpoint: unhealthy})
	require.NoError(t, err)
	require.False(t, gateway.HealthAvailable())
	_, err = gateway.Health(context.Background())
	require.Error(t, err)
}

func TestGatewayRejectsHealthAvailableWithoutHealthEndpoint(t *testing.T) {
	child := &healthDeclaredWithoutEndpoint{gatewayEndpoint: newGatewayEndpoint("declared", gatewayChildSchema, nil)}
	_, err := NewGateway("root", Namespace{Name: "declared", Endpoint: child})
	require.ErrorContains(t, err, "HealthAvailable")
}

func TestGatewayHealthAggregatesChildCapabilitiesAndDatabaseFailure(t *testing.T) {
	good := &gatewayHealthyEndpoint{gatewayEndpoint: newGatewayEndpoint("good", gatewayChildSchema, nil), report: &HealthReport{Ready: true, Degraded: true, DegradedSources: []string{"archive"}, SnapshotOK: true, DatabaseOK: true, CDCConfigured: true, SubscriptionAvailable: true}}
	bad := &gatewayHealthyEndpoint{gatewayEndpoint: newGatewayEndpoint("bad", gatewayChildSchema, nil), report: &HealthReport{SnapshotOK: true, DatabaseError: "db down"}}
	gateway, err := NewGateway("root", Namespace{Name: "good", Endpoint: good}, Namespace{Name: "bad", Endpoint: bad})
	require.NoError(t, err)
	report, err := gateway.Health(context.Background())
	require.Error(t, err)
	require.False(t, report.Ready)
	require.False(t, report.DatabaseOK)
	require.Equal(t, "bad: db down", report.DatabaseError)
	require.True(t, report.CDCConfigured)
	require.True(t, report.Degraded)
	require.Equal(t, []string{"good:archive"}, report.DegradedSources)
}

func TestGatewayPrefixesChildErrorsAndKeepsNullableNamespace(t *testing.T) {
	child := newGatewayEndpoint("crm-service", gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
		return []byte(`{"data":{"user":null},"errors":[{"message":"denied: sql=secret","path":["user"],"extensions":{"code":"FORBIDDEN","token":"secret"}}]}`), nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), `query { sales: crm { user { id } } }`, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"data":{"sales":null},
		"errors":[{"message":"endpoint execution failed","path":["sales","user"],"locations":[{"line":1,"column":22}],"extensions":{"code":"FORBIDDEN"}}]
	}`, string(body))
	require.NotContains(t, string(body), "secret")
}

func TestGatewaySanitizesUnknownChildErrorExtensions(t *testing.T) {
	err := gqlerror.Errorf("stack and token secret")
	err.Path = ast.Path{ast.PathName("user")}
	err.Extensions = map[string]any{"code": "INTERNAL_SECRET", "debug": map[string]any{"token": "secret"}}
	field := &ast.Field{Name: "crm", SelectionSet: ast.SelectionSet{&ast.Field{Name: "user"}}}
	prefixErrors(gqlerror.List{err}, "crm", field)
	require.Equal(t, "endpoint execution failed", err.Message)
	require.Equal(t, map[string]any{"code": "REMOTE_ERROR"}, err.Extensions)
	require.Equal(t, ast.Path{ast.PathName("crm"), ast.PathName("user")}, err.Path)
}

func TestGatewayRejectsNullChildErrorsWithoutPanicking(t *testing.T) {
	child := newGatewayEndpoint("crm-service", gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
		return []byte(`{"data":{"user":null},"errors":[null]}`), nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), `{ crm { user { id } } }`, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"crm":null},"errors":[{"message":"endpoint returned an invalid GraphQL response","path":["crm"],"locations":[{"line":1,"column":3}],"extensions":{"code":"BAD_GATEWAY_RESPONSE"}}]}`, string(body))
}

func TestGatewayContainsChildEndpointPanic(t *testing.T) {
	child := newGatewayEndpoint("crm-service", gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
		panic("secret child panic")
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), `{ crm { user { id } } }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(body), "REMOTE_UNAVAILABLE")
	require.NotContains(t, string(body), "secret child panic")
}

func TestGatewayStripsUnselectedChildResponseFields(t *testing.T) {
	child := newGatewayEndpoint("crm-service", gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
		return []byte(`{"data":{"user":{"id":"1","name":"hidden","secret":"must-not-leak"},"internal":"must-not-leak"}}`), nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), `{ crm { user { id } } }`, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"crm":{"user":{"id":"1"}}}}`, string(body))
}

func TestGatewayRejectsInvalidChildEnumWithoutBubblingNullableField(t *testing.T) {
	child := newGatewayEndpoint("crm-service", gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
		return []byte(`{"data":{"user":{"id":"1","status":"INTERNAL_ONLY"}}}`), nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), `{ crm { user { id status } } }`, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"data":{"crm":{"user":{"id":"1","status":null}}},
		"errors":[{"message":"endpoint returned a value with the wrong shape","path":["crm","user","status"],"locations":[{"line":1,"column":19}],"extensions":{"code":"BAD_GATEWAY_RESPONSE"}}]
	}`, string(body))
}

func TestGatewayComposesAndExecutesAbstractTypes(t *testing.T) {
	child := newGatewayEndpoint("crm", gatewayAbstractSchema, func(_ context.Context, query string, _ map[string]any) ([]byte, error) {
		require.Contains(t, query, "... on User")
		require.Contains(t, query, "... on Team")
		require.Contains(t, query, "... on Node")
		require.Contains(t, query, gatewayTypeKey+": __typename")
		require.NotContains(t, query, "Crm_User")
		return []byte(`{"data":{
			"node":{"__gql_gateway_type":"User","id":"1","name":"Ada","slug":"must-not-leak"},
			"search":[
				{"__gql_gateway_type":"User","__typename":"User","id":"1","name":"Ada","slug":"must-not-leak"},
				{"__gql_gateway_type":"Team","__typename":"Team","id":"2","name":"must-not-leak","slug":"core"}
			]
		}}`), nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	schema, err := gateway.Schema()
	require.NoError(t, err)
	require.Contains(t, schema, "interface Crm_Node")
	require.Contains(t, schema, "union Crm_SearchResult = Crm_User | Crm_Team")
	require.Contains(t, schema, "type Crm_User implements Crm_Node")
	body, err := gateway.Execute(context.Background(), `{
		crm {
			node { id ...UserFields ... on Crm_Team { slug } }
			search { __typename ... on Crm_Node { id } ... on Crm_User { name } ... on Crm_Team { slug } }
		}
	}
	fragment UserFields on Crm_User { name }
	`, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"crm":{
		"node":{"id":"1","name":"Ada"},
		"search":[{"__typename":"Crm_User","id":"1","name":"Ada"},{"__typename":"Crm_Team","id":"2","slug":"core"}]
	}}}`, string(body))
}

func TestGatewayAbstractTypeDiscriminatorCannotCollideWithClientAlias(t *testing.T) {
	child := newGatewayEndpoint("crm", gatewayAbstractSchema, func(_ context.Context, query string, _ map[string]any) ([]byte, error) {
		require.Contains(t, query, gatewayTypeKey+"_: __typename")
		return []byte(`{"data":{"node":{"__gql_gateway_type":"1","__gql_gateway_type_":"User","name":"Ada"}}}`), nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), `{ crm { node { __gql_gateway_type: id ... on Crm_User { name } } } }`, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"crm":{"node":{"__gql_gateway_type":"1","name":"Ada"}}}}`, string(body))
}

func TestGatewayRejectsMissingAbstractDiscriminator(t *testing.T) {
	child := newGatewayEndpoint("crm", gatewayAbstractSchema, func(context.Context, string, map[string]any) ([]byte, error) {
		return []byte(`{"data":{"node":{"id":"1","name":"Ada"}}}`), nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), `{ crm { node { id ... on Crm_User { name } } } }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(body), "BAD_GATEWAY_RESPONSE")
	require.Contains(t, string(body), `"crm":null`)
}

func TestGatewayCoercesIntegerIDFromChild(t *testing.T) {
	child := newGatewayEndpoint("crm-service", gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
		return []byte(`{"data":{"user":{"id":42}}}`), nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), `{ crm { user { id } } }`, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"crm":{"user":{"id":"42"}}}}`, string(body))
}

func TestGatewayExecutesNamedOperation(t *testing.T) {
	child := newGatewayEndpoint("crm-service", gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
		return []byte(`{"data":{"user":{"id":"selected"}}}`), nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	body, err := gateway.ExecuteOperation(context.Background(), `
		query First { crm { users { id } } }
		query Second { crm { user { id } } }
	`, nil, "Second")
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"crm":{"user":{"id":"selected"}}}}`, string(body))
}

func TestGatewayMutationContinuesAfterNullableErrorInDocumentOrder(t *testing.T) {
	var mu sync.Mutex
	var order []string
	endpoint := func(id string, fail bool) *gatewayEndpoint {
		return newGatewayEndpoint(id, gatewayChildSchema, func(_ context.Context, query string, _ map[string]any) ([]byte, error) {
			mu.Lock()
			order = append(order, id)
			mu.Unlock()
			alias := "first"
			for _, candidate := range []string{"first", "second", "third"} {
				if strings.Contains(query, candidate+":") {
					alias = candidate
				}
			}
			if fail {
				return []byte(fmt.Sprintf(`{"data":{"%s":null},"errors":[{"message":"rejected","path":["%s"]}],"extensions":{"mutationCommit":"rolled_back"}}`, alias, alias)), nil
			}
			return []byte(fmt.Sprintf(`{"data":{"%s":{"id":"1"}}}`, alias)), nil
		})
	}
	crm, erp := endpoint("crm", false), endpoint("erp", true)
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: crm}, Namespace{Name: "erp", Endpoint: erp})
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), `mutation($input: Crm_UserInput!, $erp: Erp_UserInput!) {
		first: crm_setName(input: $input) { id }
		second: erp_setName(input: $erp) { id }
		third: crm_setName(input: $input) { id }
	}`, map[string]any{"input": map[string]any{"name": "a"}, "erp": map[string]any{"name": "b"}})
	require.NoError(t, err)
	require.Equal(t, []string{"crm", "erp", "crm"}, order)
	require.JSONEq(t, `{
		"data":{"first":{"id":"1"},"second":null,"third":{"id":"1"}},
		"errors":[{"message":"endpoint execution failed","path":["second"],"locations":[{"line":3,"column":3}],"extensions":{"code":"REMOTE_ERROR"}}],
		"extensions":{"committedNamespaces":["crm"]}
	}`, string(body))
}

func TestGatewayMutationStopsAfterRootNonNullFailure(t *testing.T) {
	schema := strings.Replace(gatewayChildSchema, "): User }", "): User! }", 1)
	var calls atomic.Int32
	crm := newGatewayEndpoint("crm", schema, func(_ context.Context, query string, _ map[string]any) ([]byte, error) {
		calls.Add(1)
		if strings.Contains(query, "second:") {
			return []byte(`{"data":{"second":null},"errors":[{"message":"failed","path":["second"]}],"extensions":{"mutationCommit":"rolled_back"}}`), nil
		}
		alias := "first"
		if strings.Contains(query, "third:") {
			alias = "third"
		}
		return []byte(fmt.Sprintf(`{"data":{"%s":{"id":"1"}}}`, alias)), nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: crm})
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), `mutation($input: Crm_UserInput!) {
		first: crm_setName(input: $input) { id }
		second: crm_setName(input: $input) { id }
		third: crm_setName(input: $input) { id }
	}`, map[string]any{"input": map[string]any{"name": "a"}})
	require.NoError(t, err)
	require.Equal(t, int32(2), calls.Load())
	require.JSONEq(t, `{
		"data":null,
		"errors":[{"message":"endpoint execution failed","path":["second"],"locations":[{"line":3,"column":3}],"extensions":{"code":"REMOTE_ERROR"}}],
		"extensions":{"committedNamespaces":["crm"]}
	}`, string(body))
}

func TestGatewayMarksMutationTransportFailureIndeterminate(t *testing.T) {
	child := newGatewayEndpoint("crm", gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
		return nil, fmt.Errorf("connection reset after commit")
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), `mutation($input: Crm_UserInput!) {
		saved: crm_setName(input: $input) { id }
	}`, map[string]any{"input": map[string]any{"name": "a"}})
	require.NoError(t, err)
	require.NotContains(t, string(body), "connection reset")
	require.JSONEq(t, `{
		"data":{"saved":null},
		"errors":[{"message":"mutation commit status is indeterminate","path":["saved"],"locations":[{"line":2,"column":3}],"extensions":{"code":"INDETERMINATE_COMMIT"}}],
		"extensions":{"committedNamespaces":[],"indeterminateNamespaces":["crm"]}
	}`, string(body))
}

func TestGatewayConsumesRealExecutorMutationCommitMetadata(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	gateway, err := NewGateway("root", Namespace{Name: "db", EndpointID: "database", Endpoint: executor})
	require.NoError(t, err)
	mutation := `mutation { db_createUser(input: { name: "Commit", email: "gateway-commit@example.com" }) { id } }`
	body, err := gateway.Execute(context.Background(), mutation, nil)
	require.NoError(t, err)
	require.NotContains(t, string(body), "mutationCommit", "内部提交协议不得泄漏到公共响应")

	body, err = gateway.Execute(context.Background(), mutation, nil)
	require.NoError(t, err)
	require.Contains(t, string(body), `"committedNamespaces":[]`)
	require.NotContains(t, string(body), "INDETERMINATE_COMMIT")
}

func TestGatewayCompletesRootTypename(t *testing.T) {
	child := newGatewayEndpoint("crm", gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
		return []byte(`{"data":{"user":{"id":"1"}}}`), nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), `{ kind: __typename crm { user { id } } }`, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"kind":"Query","crm":{"user":{"id":"1"}}}}`, string(body))
}

func TestGatewayRejectsUnstableDuplicateAndCyclicEndpointIdentities(t *testing.T) {
	unnamed := &gatewayEndpoint{snapshot: &Snapshot{schema: gatewayChildSchema}}
	_, err := NewGateway("root", Namespace{Name: "crm", Endpoint: unnamed})
	require.ErrorContains(t, err, "稳定endpoint identity")

	child := newGatewayEndpoint("shared", gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) { return nil, nil })
	_, err = NewGateway("root", Namespace{Name: "crm", Endpoint: child}, Namespace{Name: "erp", Endpoint: child})
	require.ErrorContains(t, err, "identity重复")

	_, err = NewGateway("root", Namespace{Name: "crm", Endpoint: newGatewayEndpoint("root", gatewayChildSchema, nil)})
	require.ErrorContains(t, err, "identity重复")

	leaf, err := NewGateway("leaf", Namespace{Name: "crm", Endpoint: newGatewayEndpoint("database", gatewayChildSchema, nil)})
	require.NoError(t, err)
	_, err = NewGateway("database", Namespace{Name: "nested", Endpoint: leaf})
	require.ErrorContains(t, err, "拓扑循环")
}

func TestGatewayRefreshAtomicallyKeepsInflightAndLastKnownGoodSnapshots(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	oldEndpoint := newGatewayEndpoint("crm-v1", gatewayChildSchema, func(ctx context.Context, _ string, _ map[string]any) ([]byte, error) {
		once.Do(func() { close(started) })
		select {
		case <-release:
			return []byte(`{"data":{"user":{"id":"old"}}}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	newEndpoint := newGatewayEndpoint("crm-v2", gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
		return []byte(`{"data":{"user":{"id":"new"}}}`), nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: oldEndpoint})
	require.NoError(t, err)
	oldSnapshot, err := gateway.Snapshot()
	require.NoError(t, err)
	oldResult := make(chan []byte, 1)
	go func() {
		body, _ := gateway.Execute(context.Background(), `{ crm { user { id } } }`, nil)
		oldResult <- body
	}()
	<-started
	require.NoError(t, gateway.Refresh(Namespace{Name: "crm", Endpoint: newEndpoint}))
	close(release)
	oldRevision, err := gateway.ForSnapshot(oldSnapshot.Fingerprint())
	require.NoError(t, err)
	pinned, err := oldRevision.ExecuteOperation(context.Background(), `{ crm { user { id } } }`, nil, "")
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"crm":{"user":{"id":"old"}}}}`, string(pinned))
	require.JSONEq(t, `{"data":{"crm":{"user":{"id":"old"}}}}`, string(<-oldResult))
	body, err := gateway.Execute(context.Background(), `{ crm { user { id } } }`, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"crm":{"user":{"id":"new"}}}}`, string(body))

	invalid := newGatewayEndpoint("broken", "directive @private on FIELD_DEFINITION\ntype Query { user: String @private }", nil)
	require.Error(t, gateway.Refresh(Namespace{Name: "crm", Endpoint: invalid}))
	body, err = gateway.Execute(context.Background(), `{ crm { user { id } } }`, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"crm":{"user":{"id":"new"}}}}`, string(body))
}

func TestParentGatewayPinsChildGatewayRevisionUntilRefresh(t *testing.T) {
	endpoint := func(id string) *gatewayEndpoint {
		return newGatewayEndpoint(id, gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"data":{"user":{"id":%q}}}`, id)), nil
		})
	}
	child, err := NewGateway("child", Namespace{Name: "crm", Endpoint: endpoint("v1")})
	require.NoError(t, err)
	parent, err := NewGateway("parent", Namespace{Name: "nested", Endpoint: child})
	require.NoError(t, err)
	require.NoError(t, child.Refresh(Namespace{Name: "crm", Endpoint: endpoint("v2")}))

	query := `{ nested { crm { user { id } } } }`
	body, err := parent.Execute(context.Background(), query, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"nested":{"crm":{"user":{"id":"v1"}}}}}`, string(body))
	require.NoError(t, parent.Refresh(Namespace{Name: "nested", Endpoint: child}))
	body, err = parent.Execute(context.Background(), query, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"nested":{"crm":{"user":{"id":"v2"}}}}}`, string(body))
}

func TestGatewayApplyRejectsStaleAndConflictingClusterUpdates(t *testing.T) {
	endpoint := func(id string) *gatewayEndpoint { return newGatewayEndpoint(id, gatewayChildSchema, nil) }
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: endpoint("v1")})
	require.NoError(t, err)
	_, err = gateway.Apply(GatewayUpdate{Revision: 1, Namespaces: []Namespace{{Name: "crm", Endpoint: endpoint("control-v1")}}})
	require.NoError(t, err)
	update := GatewayUpdate{Revision: 3, Namespaces: []Namespace{{Name: "crm", Endpoint: endpoint("v3")}}}
	snapshot, err := gateway.Apply(update)
	require.NoError(t, err)
	require.NotNil(t, snapshot)

	_, err = gateway.Apply(update)
	require.NoError(t, err, "an identical control-plane delivery must be idempotent")
	_, err = gateway.Apply(GatewayUpdate{Revision: 3, Namespaces: []Namespace{{Name: "crm", Endpoint: endpoint("split-brain")}}})
	require.ErrorIs(t, err, ErrControlConflict)
	_, err = gateway.Apply(GatewayUpdate{Revision: 2, Namespaces: []Namespace{{Name: "crm", Endpoint: endpoint("v2")}}})
	require.ErrorIs(t, err, ErrControlStale)
}

func TestGAGatewayApplyCannotRollBackUnderConcurrentControlPlaneLoad(t *testing.T) {
	endpoint := func(revision uint64) *gatewayEndpoint {
		return newGatewayEndpoint(fmt.Sprintf("v%d", revision), gatewayChildSchema, nil)
	}
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: endpoint(1)})
	require.NoError(t, err)
	var wait sync.WaitGroup
	for revision := uint64(2); revision <= 65; revision++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _ = gateway.Apply(GatewayUpdate{Revision: revision, Namespaces: []Namespace{{Name: "crm", Endpoint: endpoint(revision)}}})
		}()
	}
	wait.Wait()
	require.EqualValues(t, 65, gateway.revision)
	require.Equal(t, "v65", gateway.current().contracts["crm"].id)
}

func TestGatewayExposesOnlyExecutableSingleRootSubscriptions(t *testing.T) {
	frames := make(chan []byte, 1)
	frames <- []byte(`{"data":{"changed":{"__typename":"User","id":"1","secret":"hidden"}}}`)
	close(frames)
	subscriber := &gatewaySubscriptionEndpoint{
		gatewayEndpoint: newGatewayEndpoint("crm", gatewaySubscriptionSchema, nil),
		events:          frames,
	}
	nonSubscriber := newGatewayEndpoint("erp", gatewaySubscriptionSchema, nil)
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: subscriber}, Namespace{Name: "erp", Endpoint: nonSubscriber})
	require.NoError(t, err)
	schema, err := gateway.Schema()
	require.NoError(t, err)
	require.Contains(t, schema, "crm: Crm_Subscription")
	require.NotContains(t, schema, "erp: Erp_Subscription")
	require.True(t, gateway.SubscriptionAvailable())

	events, err := gateway.Subscribe(context.Background(), `subscription { crm { changed { __typename id } } }`, nil, "")
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"crm":{"changed":{"__typename":"Crm_User","id":"1"}}}}`, string(<-events))
	_, open := <-events
	require.False(t, open)

	_, err = gateway.Subscribe(context.Background(), `subscription { crm { changed { id } removed { id } } }`, nil, "")
	require.ErrorContains(t, err, "child根字段")
}

func TestGatewayValidatesSubscriptionAgainstChildEffectiveArtifact(t *testing.T) {
	frames := make(chan []byte, 1)
	frames <- []byte(`{"data":{"changed":{"id":"1"}}}`)
	close(frames)
	base := &gatewaySubscriptionEndpoint{gatewayEndpoint: newGatewayEndpoint("crm", gatewaySubscriptionSchema, nil), events: frames}
	effectiveSchema := strings.Replace(gatewaySubscriptionSchema, ", removed: User", "", 1)
	child := &gatewayEffectiveSubscriptionEndpoint{gatewaySubscriptionEndpoint: base, effective: SchemaArtifact{Schema: effectiveSchema, Fingerprint: digest(effectiveSchema)}}
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)

	_, err = gateway.Subscribe(context.Background(), `subscription { crm { removed { id } } }`, nil, "")
	require.ErrorContains(t, err, `Cannot query field "removed"`)
	events, err := gateway.Subscribe(context.Background(), `subscription { crm { changed { id } } }`, nil, "")
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"crm":{"changed":{"id":"1"}}}}`, string(<-events))
}

func newGatewayEndpoint(id, schema string, execute func(context.Context, string, map[string]any) ([]byte, error)) *gatewayEndpoint {
	return &gatewayEndpoint{id: id, snapshot: &Snapshot{schema: schema, fingerprint: id}, execute: execute}
}

func (my *gatewayEndpoint) EndpointID() string           { return my.id }
func (my *gatewayEndpoint) Snapshot() (*Snapshot, error) { return my.snapshot, nil }
func (my *gatewayEndpoint) Execute(ctx context.Context, query string, variables map[string]any) ([]byte, error) {
	my.calls.Add(1)
	return my.execute(ctx, query, variables)
}
func (my *gatewayEndpoint) ExecuteOperation(ctx context.Context, query string, variables map[string]any, _ string) ([]byte, error) {
	return my.Execute(ctx, query, variables)
}

func TestGatewayPrefixesImportedTypesAndIntrospectsNamespaces(t *testing.T) {
	child := func(id string) *gatewayEndpoint {
		return newGatewayEndpoint(id, gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
			return []byte(`{"data":{"user":{"id":"1","name":"ok","status":"ACTIVE"}}}`), nil
		})
	}
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child("crm-service")}, Namespace{Name: "erp", Endpoint: child("erp-service")})
	require.NoError(t, err)
	schema, err := gateway.Schema()
	require.NoError(t, err)
	for _, expected := range []string{"crm: Crm_Query", "erp: Erp_Query", "type Crm_User", "type Erp_User", "input Crm_UserInput", "input Erp_UserInput", "enum Crm_Status", "enum Erp_Status"} {
		require.Contains(t, schema, expected)
	}
	body, err := gateway.Execute(context.Background(), `{ __type(name: "Crm_User") { name fields { name } } }`, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"__type":{"name":"Crm_User","fields":[{"name":"id"},{"name":"name"},{"name":"status"},{"name":"payload"}]}}}`, string(body))
}

func TestGatewaySnapshotCoversEveryIntrospectableCoordinate(t *testing.T) {
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: newGatewayEndpoint("crm", gatewayChildSchema, nil)})
	require.NoError(t, err)
	snapshot, err := gateway.Snapshot()
	require.NoError(t, err)
	for _, definition := range gateway.current().schema.Types {
		if definition.Kind != ast.Object && definition.Kind != ast.Interface {
			continue
		}
		for _, field := range definition.Fields {
			coordinate := definition.Name + "." + field.Name
			capability, ok := snapshot.Capability(coordinate)
			require.True(t, ok, coordinate)
			require.NotEmpty(t, capability.Handler, coordinate)
		}
	}
}

func TestGatewayPreservesChildCapabilitySemanticsWhenRenaming(t *testing.T) {
	child := newGatewayEndpoint("crm", gatewayChildSchema, nil)
	child.snapshot.capabilities = []Capability{
		{Coordinate: "Query.user", Parent: "Query", Name: "user", Type: "User!", Handler: HandlerResolver, Planner: "child", Resource: "users", Actions: []string{"select"}, Cost: 7, PolicyAware: true, Timeout: 3 * time.Second, RetrySafe: true},
		{Coordinate: "User.name", Parent: "User", Name: "name", Type: "String", Handler: HandlerResolver, Planner: "child", Resource: "users", Actions: []string{"select"}, Cost: 11, Timeout: time.Second},
	}
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	snapshot, err := gateway.Snapshot()
	require.NoError(t, err)
	query, ok := snapshot.Capability("Crm_Query.user")
	require.True(t, ok)
	require.Equal(t, uint32(7), query.Cost)
	require.Equal(t, []string{"select"}, query.Actions)
	require.Equal(t, "users", query.Resource)
	require.True(t, query.PolicyAware)
	require.Equal(t, 3*time.Second, query.Timeout)
	field, ok := snapshot.Capability("Crm_User.name")
	require.True(t, ok)
	require.Equal(t, uint32(11), field.Cost)
}

func TestGatewayEffectiveSnapshotPreservesAllowedChildCapabilitySemantics(t *testing.T) {
	child := newGatewayEndpoint("crm", gatewayChildSchema, nil)
	child.snapshot.capabilities = []Capability{{Coordinate: "Query.user", Parent: "Query", Name: "user", Type: "User!", Handler: HandlerResolver, Planner: "child", Resource: "users", Actions: []string{"select"}, Cost: 7, PolicyAware: true}}
	child.snapshot.index = map[string]Capability{"Query.user": child.snapshot.capabilities[0]}
	effectiveSchema := strings.Replace(gatewayChildSchema, ", status: Status", "", 1)
	endpoint := &gatewayEffectiveEndpoint{gatewayEndpoint: child, effective: SchemaArtifact{Schema: effectiveSchema, Fingerprint: digest(effectiveSchema), Capabilities: child.snapshot.Capabilities()}}
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: endpoint})
	require.NoError(t, err)
	state, err := gateway.current().effective(context.Background())
	require.NoError(t, err)
	capability, ok := state.snapshot.Capability("Crm_Query.user")
	require.True(t, ok)
	require.Equal(t, uint32(7), capability.Cost)
	require.Equal(t, "users", capability.Resource)
	require.True(t, capability.PolicyAware)
}

func TestGatewayEffectiveSnapshotFingerprintIncludesCapabilities(t *testing.T) {
	child := newGatewayEndpoint("crm", gatewayChildSchema, nil)
	capability := Capability{Coordinate: "Query.user", Parent: "Query", Name: "user", Type: "User!", Handler: HandlerResolver, Cost: 1}
	endpoint := &gatewayEffectiveEndpoint{gatewayEndpoint: child, effective: SchemaArtifact{
		Schema: gatewayChildSchema, Fingerprint: digest(gatewayChildSchema), Capabilities: []Capability{capability},
	}}
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: endpoint})
	require.NoError(t, err)
	first, err := gateway.current().effective(context.Background())
	require.NoError(t, err)
	endpoint.effective.Capabilities[0].Cost = 2
	second, err := gateway.current().effective(context.Background())
	require.NoError(t, err)
	require.NotEqual(t, first.snapshot.Fingerprint(), second.snapshot.Fingerprint())
}

func TestGatewayIntrospectionUsesChildEffectiveArtifact(t *testing.T) {
	child := newGatewayEndpoint("crm", gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
		return []byte(`{"data":{"user":{"id":"1"}}}`), nil
	})
	effectiveSchema := strings.Replace(gatewayChildSchema, ", status: Status", "", 1)
	endpoint := &gatewayEffectiveEndpoint{gatewayEndpoint: child, effective: SchemaArtifact{Schema: effectiveSchema, Fingerprint: digest(effectiveSchema)}}
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: endpoint})
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), `{ __type(name: "Crm_User") { fields { name } } }`, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"__type":{"fields":[{"name":"id"},{"name":"name"},{"name":"payload"}]}}}`, string(body))
}

func TestNestedGatewayPropagatesChildEffectiveSchema(t *testing.T) {
	child := newGatewayEndpoint("crm", gatewayChildSchema, nil)
	effectiveSchema := strings.Replace(gatewayChildSchema, ", status: Status", "", 1)
	inner, err := NewGateway("inner", Namespace{Name: "crm", Endpoint: &gatewayEffectiveEndpoint{
		gatewayEndpoint: child,
		effective:       SchemaArtifact{Schema: effectiveSchema, Fingerprint: digest(effectiveSchema)},
	}})
	require.NoError(t, err)
	outer, err := NewGateway("outer", Namespace{Name: "platform", Endpoint: inner})
	require.NoError(t, err)
	body, err := outer.Execute(context.Background(), `{ __type(name: "Platform_Crm_User") { fields { name } } }`, nil)
	require.NoError(t, err)
	require.NotContains(t, string(body), `"name":"status"`)
}

func TestGatewayBindsEffectiveExecutionPinToChildContext(t *testing.T) {
	child := newGatewayEndpoint("crm", gatewayChildSchema, func(ctx context.Context, _ string, _ map[string]any) ([]byte, error) {
		pin, ok := ExecutionPinFromContext(ctx)
		if !ok || pin == "" {
			t.Fatalf("child context missing effective execution pin")
		}
		return []byte(`{"data":{"user":{"id":"1"}}}`), nil
	})
	artifact := SchemaArtifact{Version: SchemaArtifactVersion, Schema: gatewayChildSchema, Fingerprint: digest(gatewayChildSchema), StructuralFingerprint: child.snapshot.Fingerprint()}
	artifact.ExecutionPin = schemaArtifactExecutionPin(artifact)
	endpoint := &gatewayEffectiveEndpoint{gatewayEndpoint: child, effective: artifact}
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: endpoint})
	require.NoError(t, err)
	_, err = gateway.Execute(context.Background(), `{ crm { user { id } } }`, nil)
	require.NoError(t, err)
}

func TestGatewayAppliesResponseLimitAtExecutionBoundary(t *testing.T) {
	child := newGatewayEndpoint("crm", gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
		return []byte(`{"data":{"user":{"id":"12345678901234567890","name":"abcdefghijklmnopqrstuvwxyz"}}}`), nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	body, err := gateway.Execute(WithResponseLimit(context.Background(), 32), `{ crm { user { id name } } }`, nil)
	require.Nil(t, body)
	require.ErrorContains(t, err, "response size limit")
}

func TestGatewaySharesResponseLimitAcrossConcurrentNamespaces(t *testing.T) {
	body := []byte(`{"data":{"user":{"id":"x"},"noise":"` + strings.Repeat("x", 180) + `"}}`)
	makeChild := func(name string) *gatewayEndpoint {
		return newGatewayEndpoint(name, gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
			return body, nil
		})
	}
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: makeChild("crm")}, Namespace{Name: "erp", Endpoint: makeChild("erp")})
	require.NoError(t, err)
	result, err := gateway.Execute(WithResponseLimit(context.Background(), int64(len(body)+32)), `{ crm { user { id } } erp { user { id } } }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(result), `"code":"LIMIT_EXCEEDED"`)
}

func TestGatewayBoundsConcurrentChildResponses(t *testing.T) {
	started := make(chan struct{}, gatewayMaxFanout)
	release := make(chan struct{})
	namespaces := make([]Namespace, 0, gatewayMaxConcurrent+2)
	var query strings.Builder
	query.WriteByte('{')
	for i := range gatewayMaxConcurrent + 2 {
		name := fmt.Sprintf("n%d", i)
		child := newGatewayEndpoint(name, gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
			started <- struct{}{}
			<-release
			return []byte(`{"data":{"user":{"id":"x"}}}`), nil
		})
		namespaces = append(namespaces, Namespace{Name: name, Endpoint: child})
		fmt.Fprintf(&query, " %s { user { id } }", name)
	}
	query.WriteByte('}')
	gateway, err := NewGateway("root", namespaces...)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, executeErr := gateway.Execute(context.Background(), query.String(), nil)
		done <- executeErr
	}()
	for range gatewayMaxConcurrent {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("gateway did not fill bounded child window")
		}
	}
	select {
	case <-started:
		t.Fatal("gateway exceeded bounded child window")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-done)
}

func TestGatewaySharesResponseLimitAcrossMutationsInOrder(t *testing.T) {
	body := []byte(`{"data":{"setName":{"id":"x"},"noise":"` + strings.Repeat("x", 300) + `"}}`)
	makeChild := func(name string) *gatewayEndpoint {
		return newGatewayEndpoint(name, gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
			return body, nil
		})
	}
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: makeChild("crm")}, Namespace{Name: "erp", Endpoint: makeChild("erp")})
	require.NoError(t, err)
	result, err := gateway.Execute(WithResponseLimit(context.Background(), 500), `mutation { crm_setName(input: { name: "a" }) { id } erp_setName(input: { name: "b" }) { id } }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(result), `"code":"LIMIT_EXCEEDED"`)
	require.Contains(t, string(result), `"indeterminateNamespaces":["erp"]`)
	require.NotContains(t, string(result), `"committedNamespaces":["crm","erp"]`)
}

func TestGatewaySubscriptionAppliesResponseLimitAtExecutionBoundary(t *testing.T) {
	events := make(chan []byte, 1)
	events <- []byte(`{"data":{"changed":{"id":"` + strings.Repeat("x", 1024) + `"}}}`)
	close(events)
	child := newGatewayEndpoint("crm", gatewaySubscriptionSchema, nil)
	subscriber := &gatewaySubscriptionEndpoint{gatewayEndpoint: child, events: events}
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: subscriber})
	require.NoError(t, err)
	stream, err := gateway.Subscribe(WithResponseLimit(context.Background(), 256), `subscription { crm { changed { id } } }`, nil, "")
	require.NoError(t, err)
	body, open := <-stream
	require.True(t, open)
	require.Contains(t, string(body), "LIMIT_EXCEEDED")
	require.LessOrEqual(t, len(body), 256)
	_, open = <-stream
	require.False(t, open)
}

func TestStaticNestedGatewayAvoidsEffectiveSchemaHotPath(t *testing.T) {
	inner, err := NewGateway("inner", Namespace{Name: "crm", Endpoint: newGatewayEndpoint("crm", gatewayChildSchema, nil)})
	require.NoError(t, err)
	outer, err := NewGateway("outer", Namespace{Name: "platform", Endpoint: inner})
	require.NoError(t, err)
	require.False(t, outer.current().identityAware)
}

func TestGatewayRejectsUnmarkedStaticEndpointWhenIdentityAwareChildIsPresent(t *testing.T) {
	child := newGatewayEndpoint("effective", gatewayChildSchema, nil)
	effectiveSchema := strings.Replace(gatewayChildSchema, ", status: Status", "", 1)
	effective := &gatewayEffectiveEndpoint{gatewayEndpoint: child, effective: SchemaArtifact{Schema: effectiveSchema, Fingerprint: digest(effectiveSchema)}}
	static := newGatewayEndpoint("static", gatewayChildSchema, nil)
	_, err := NewGateway("root", Namespace{Name: "effective", Endpoint: effective}, Namespace{Name: "static", Endpoint: static})
	require.ErrorContains(t, err, "identity-aware effective schema")
}

func TestGatewayRejectsEffectiveCapabilityWithoutEffectiveEndpoint(t *testing.T) {
	// The endpoint claims identity-aware materialization but does not expose it.
	_, err := NewGateway("root", Namespace{Name: "declared", Endpoint: effectiveCapabilityEndpoint{gatewayEndpoint: newGatewayEndpoint("declared", gatewayChildSchema, nil)}})
	require.ErrorContains(t, err, "EffectiveSchemaEndpoint")
}

func TestGatewayAllowsExplicitStaticEndpointWhenIdentityAwareChildIsPresent(t *testing.T) {
	child := newGatewayEndpoint("effective", gatewayChildSchema, nil)
	effectiveSchema := strings.Replace(gatewayChildSchema, ", status: Status", "", 1)
	effective := &gatewayEffectiveEndpoint{gatewayEndpoint: child, effective: SchemaArtifact{Schema: effectiveSchema, Fingerprint: digest(effectiveSchema)}}
	static := &gatewayExplicitStaticEndpoint{gatewayEndpoint: newGatewayEndpoint("static", gatewayChildSchema, nil)}
	_, err := NewGateway("root", Namespace{Name: "effective", Endpoint: effective}, Namespace{Name: "static", Endpoint: static})
	require.NoError(t, err)
}

func TestGatewayRejectsForgedEffectiveArtifactFingerprint(t *testing.T) {
	child := newGatewayEndpoint("crm", gatewayChildSchema, nil)
	endpoint := &gatewayEffectiveEndpoint{gatewayEndpoint: child, effective: SchemaArtifact{Schema: gatewayChildSchema, Fingerprint: "forged"}}
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: endpoint})
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), `{ crm { user { id } } }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(body), `"code":"BAD_GATEWAY_RESPONSE"`)
	require.NotContains(t, string(body), "fingerprint mismatch")
	require.Zero(t, child.calls.Load())
}

func TestGatewayRejectsEffectiveArtifactWithoutVersionOrExecutionPin(t *testing.T) {
	child := newGatewayEndpoint("crm", gatewayChildSchema, nil)
	endpoint := &gatewayRawEffectiveEndpoint{gatewayEndpoint: child, artifact: SchemaArtifact{Schema: gatewayChildSchema, Fingerprint: digest(gatewayChildSchema)}}
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: endpoint})
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), `{ crm { user { id } } }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(body), `"code":"BAD_GATEWAY_RESPONSE"`)
	require.NotContains(t, string(body), "execution pin mismatch")
	require.Zero(t, child.calls.Load())
}

func TestGatewayDoesNotCacheIdentityEffectiveSchemaWithoutCacheIdentity(t *testing.T) {
	child := newGatewayEndpoint("crm", gatewayChildSchema, nil)
	var calls atomic.Int32
	endpoint := &gatewayEffectiveEndpoint{gatewayEndpoint: child, effectiveFn: func(ctx context.Context) (SchemaArtifact, error) {
		calls.Add(1)
		identity, ok := IdentityFromContext(ctx)
		if !ok {
			return SchemaArtifact{}, fmt.Errorf("identity required")
		}
		schema := gatewayChildSchema
		if identity.Tenant() == "tenant-b" {
			schema = strings.Replace(schema, ", status: Status", "", 1)
		}
		return SchemaArtifact{Schema: schema, Fingerprint: digest(schema), Identity: IdentityDescriptorFromIdentity(identity)}, nil
	}}
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: endpoint})
	require.NoError(t, err)
	a, err := NewIdentity(IdentityInput{Tenant: "tenant-a", Subject: "user"})
	require.NoError(t, err)
	b, err := NewIdentity(IdentityInput{Tenant: "tenant-b", Subject: "user"})
	require.NoError(t, err)
	_, err = gateway.EffectiveSchema(WithIdentity(context.Background(), a))
	require.NoError(t, err)
	second, err := gateway.EffectiveSchema(WithIdentity(context.Background(), b))
	require.NoError(t, err)
	require.Equal(t, int32(2), calls.Load(), "an uncacheable child must be materialized per identity")
	require.NotContains(t, second.Schema, "status")
}

func TestGatewayRejectsEffectiveArtifactForDifferentIdentity(t *testing.T) {
	child := newGatewayEndpoint("crm", gatewayChildSchema, nil)
	artifact := FinalizeSchemaArtifact(SchemaArtifact{Schema: gatewayChildSchema, Fingerprint: digest(gatewayChildSchema)})
	endpoint := &gatewayRawEffectiveEndpoint{gatewayEndpoint: child, artifact: artifact}
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: endpoint})
	require.NoError(t, err)
	identity, err := NewIdentity(IdentityInput{Tenant: "tenant-a", Subject: "user-a"})
	require.NoError(t, err)
	body, err := gateway.Execute(WithIdentity(context.Background(), identity), `{ crm { user { id } } }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(body), `"code":"BAD_GATEWAY_RESPONSE"`)
	require.Zero(t, child.calls.Load())
}

func TestGatewayValidatesDataAndMixedIntrospectionAgainstChildEffectiveArtifact(t *testing.T) {
	var calls atomic.Int32
	child := newGatewayEndpoint("crm", gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
		calls.Add(1)
		return []byte(`{"data":{"user":{"status":"ACTIVE"}}}`), nil
	})
	effectiveSchema := strings.Replace(gatewayChildSchema, ", status: Status", "", 1)
	endpoint := &gatewayEffectiveEndpoint{gatewayEndpoint: child, effective: SchemaArtifact{Schema: effectiveSchema, Fingerprint: digest(effectiveSchema)}}
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: endpoint})
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), `{ crm { user { status } } __type(name: "Crm_User") { fields { name } } }`, nil)
	require.NoError(t, err)
	require.Contains(t, string(body), `Cannot query field \"status\"`)
	require.Zero(t, calls.Load())
}

func TestGatewayRunsNamespacesInParallelAndBatchesOneNamespace(t *testing.T) {
	var barrier sync.WaitGroup
	barrier.Add(2)
	endpoint := func(id string) *gatewayEndpoint {
		return newGatewayEndpoint(id, gatewayChildSchema, func(ctx context.Context, query string, _ map[string]any) ([]byte, error) {
			barrier.Done()
			done := make(chan struct{})
			go func() { barrier.Wait(); close(done) }()
			select {
			case <-done:
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Second):
				return nil, fmt.Errorf("namespace query was serialized")
			}
			if strings.Count(query, "user") < 2 {
				return nil, fmt.Errorf("same namespace fields were not batched: %s", query)
			}
			return []byte(`{"data":{"first":{"id":"1"},"second":{"id":"2"}}}`), nil
		})
	}
	crm, erp := endpoint("crm-service"), endpoint("erp-service")
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: crm}, Namespace{Name: "erp", Endpoint: erp})
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), `{
		crm { first: user { id } second: user { id } }
		erp { first: user { id } second: user { id } }
	}`, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"crm":{"first":{"id":"1"},"second":{"id":"2"}},"erp":{"first":{"id":"1"},"second":{"id":"2"}}}}`, string(body))
	require.Equal(t, int32(1), crm.calls.Load())
	require.Equal(t, int32(1), erp.calls.Load())
}

func TestGatewayLoadsEffectiveNamespacesInParallel(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	endpoint := func(id string) *gatewayEffectiveEndpoint {
		base := newGatewayEndpoint(id, gatewayChildSchema, nil)
		artifact := SchemaArtifact{Schema: gatewayChildSchema, Fingerprint: digest(gatewayChildSchema), StructuralFingerprint: base.snapshot.Fingerprint(), Capabilities: base.snapshot.Capabilities()}
		return &gatewayEffectiveEndpoint{gatewayEndpoint: base, effectiveFn: func(context.Context) (SchemaArtifact, error) {
			started <- struct{}{}
			<-release
			return artifact, nil
		}}
	}
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: endpoint("crm")}, Namespace{Name: "erp", Endpoint: endpoint("erp")})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { _, err := gateway.current().effective(context.Background()); done <- err }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("effective schemas were loaded serially")
		}
	}
	releaseOnce.Do(func() { close(release) })
	require.NoError(t, <-done)
}

func TestGatewayEffectiveCachePrefetchesStableChildIdentity(t *testing.T) {
	child := &gatewayCacheIdentityEndpoint{
		gatewayEffectiveEndpoint: &gatewayEffectiveEndpoint{
			gatewayEndpoint: newGatewayEndpoint("crm", gatewayChildSchema, nil),
			effective: SchemaArtifact{Schema: gatewayChildSchema, Fingerprint: digest(gatewayChildSchema),
				StructuralFingerprint: "crm-snapshot", CacheIdentity: "stable"},
		},
		cacheKey: "stable",
	}
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	ctx := context.Background()
	_, err = gateway.EffectiveSchema(ctx)
	require.NoError(t, err)
	_, err = gateway.EffectiveSchema(ctx)
	require.NoError(t, err)
	require.Equal(t, int32(1), child.effectiveCalls.Load())
}

func TestGatewaySingleNamespaceExecutesInline(t *testing.T) {
	caller := gatewayGoroutineID()
	var childGoroutine atomic.Int64
	child := newGatewayEndpoint("crm", gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
		childGoroutine.Store(gatewayGoroutineID())
		return []byte(`{"data":{"user":{"id":"1"}}}`), nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	_, err = gateway.Execute(context.Background(), `{ crm { user { id } } }`, nil)
	require.NoError(t, err)
	require.Equal(t, caller, childGoroutine.Load(), "single namespace execution should stay on the caller goroutine")
}

func gatewayGoroutineID() int64 {
	stack := make([]byte, 64)
	n := runtime.Stack(stack, false)
	fields := strings.Fields(string(stack[:n]))
	if len(fields) < 2 {
		return 0
	}
	id, _ := strconv.ParseInt(fields[1], 10, 64)
	return id
}

func TestGatewayBatchesRepeatedNamespaceAliasesIntoOneChildOperation(t *testing.T) {
	child := newGatewayEndpoint("crm", gatewayChildSchema, func(_ context.Context, query string, _ map[string]any) ([]byte, error) {
		require.Contains(t, query, "_g0_0: user")
		require.Contains(t, query, "_g1_0: users")
		return []byte(`{"data":{"_g0_0":{"id":"1"},"_g1_0":[{"id":"2"}]}}`), nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), `{ first: crm { user { id } } second: crm { users { id } } }`, nil)
	require.NoError(t, err)
	require.Equal(t, int32(1), child.calls.Load())
	require.JSONEq(t, `{"data":{"first":{"user":{"id":"1"}},"second":{"users":[{"id":"2"}]}}}`, string(body))
}

func TestGatewayRejectsExcessiveFanoutBeforeCallingChildren(t *testing.T) {
	namespaces := make([]Namespace, gatewayMaxFanout+1)
	var query strings.Builder
	query.WriteString("{")
	for index := range namespaces {
		name := fmt.Sprintf("n%d", index)
		namespaces[index] = Namespace{Name: name, Endpoint: newGatewayEndpoint(name, gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
			t.Fatal("child called before fan-out rejection")
			return nil, nil
		})}
		fmt.Fprintf(&query, " %s { user { id } }", name)
	}
	query.WriteString(" }")
	gateway, err := NewGateway("root", namespaces...)
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), query.String(), nil)
	require.NoError(t, err)
	require.Contains(t, string(body), "LIMIT_EXCEEDED")
	require.NotContains(t, string(body), `"data"`)
}

func TestGatewayRejectsOversizedEmbeddedRequestBeforeCallingChild(t *testing.T) {
	child := newGatewayEndpoint("crm", gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
		t.Fatal("child called before request limit rejection")
		return nil, nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)

	body, err := gateway.Execute(context.Background(), `{ crm { user { id } } }`, map[string]any{"input": strings.Repeat("x", gatewayMaxVariableBytes+1)})
	require.NoError(t, err)
	require.Contains(t, string(body), "LIMIT_EXCEEDED")
	require.Zero(t, child.calls.Load())

	body, err = gateway.Execute(context.Background(), "#"+strings.Repeat("x", maxDocumentBytes)+"\n{ crm { user { id } } }", nil)
	require.NoError(t, err)
	require.Contains(t, string(body), "LIMIT_EXCEEDED")
	require.Zero(t, child.calls.Load())
}

func TestGatewayCachesStaticOperationAndLimitsExpandedRootFragments(t *testing.T) {
	child := newGatewayEndpoint("crm", gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
		return []byte(`{"data":{"user":{"id":"1"}}}`), nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	query := `{ crm { user { id } } }`
	for range 2 {
		_, err = gateway.Execute(context.Background(), query, nil)
		require.NoError(t, err)
	}
	state := gateway.current()
	_, cached := state.cache.Get(planKeyFor("", query, "", state.snapshot.Fingerprint()))
	require.True(t, cached)

	var document strings.Builder
	document.WriteString("query { ...Many } fragment Many on Query {")
	for index := range gatewayMaxRootFields + 1 {
		fmt.Fprintf(&document, " n%d: crm { user { id } }", index)
	}
	document.WriteString(" }")
	body, err := gateway.Execute(context.Background(), document.String(), nil)
	require.NoError(t, err)
	require.Contains(t, string(body), "LIMIT_EXCEEDED")
	require.Equal(t, int32(2), child.calls.Load())
}

func TestGatewayDoesNotCacheVariableConditionalSelection(t *testing.T) {
	child := newGatewayEndpoint("crm", gatewayChildSchema, func(_ context.Context, query string, _ map[string]any) ([]byte, error) {
		return []byte(`{"data":{"user":{"id":"1","name":"Ada"}}}`), nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	query := `query($show: Boolean!) { crm { user { id name @include(if: $show) } } }`
	body, err := gateway.Execute(context.Background(), query, map[string]any{"show": false})
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"crm":{"user":{"id":"1"}}}}`, string(body))
	body, err = gateway.Execute(context.Background(), query, map[string]any{"show": true})
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"crm":{"user":{"id":"1","name":"Ada"}}}}`, string(body))
	_, cached := gateway.current().cache.Get(planKeyFor("", query, "", ""))
	require.False(t, cached)
}

func TestGatewayCachedOperationIsConcurrentReadOnly(t *testing.T) {
	child := newGatewayEndpoint("crm", gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
		return []byte(`{"data":{"user":{"id":"1"}}}`), nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	query := `{ crm { user { id } } }`
	_, err = gateway.Execute(context.Background(), query, nil)
	require.NoError(t, err)
	var wait sync.WaitGroup
	for range 64 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			body, executeErr := gateway.Execute(context.Background(), query, nil)
			require.NoError(t, executeErr)
			require.JSONEq(t, `{"data":{"crm":{"user":{"id":"1"}}}}`, string(body))
		}()
	}
	wait.Wait()
}

func BenchmarkGatewayWarmQuery(b *testing.B) {
	child := newGatewayEndpoint("crm", gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
		return []byte(`{"data":{"user":{"id":"1"}}}`), nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	if err != nil {
		b.Fatal(err)
	}
	query := `{ crm { user { id } } }`
	if _, err = gateway.Execute(context.Background(), query, nil); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err = gateway.Execute(context.Background(), query, nil); err != nil {
			b.Fatal(err)
		}
	}
}
