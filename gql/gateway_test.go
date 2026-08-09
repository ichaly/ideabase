package gql

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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

type gatewayHealthyEndpoint struct{ *gatewayEndpoint }

func (my *gatewayHealthyEndpoint) Health(context.Context) (HealthReport, error) {
	return HealthReport{Ready: true, SnapshotOK: true, DatabaseOK: true, SnapshotFingerprint: my.snapshot.Fingerprint()}, nil
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

func TestGatewayPrefixesChildErrorsAndKeepsNullableNamespace(t *testing.T) {
	child := newGatewayEndpoint("crm-service", gatewayChildSchema, func(context.Context, string, map[string]any) ([]byte, error) {
		return []byte(`{"data":{"user":null},"errors":[{"message":"denied","path":["user"],"extensions":{"code":"FORBIDDEN"}}]}`), nil
	})
	gateway, err := NewGateway("root", Namespace{Name: "crm", Endpoint: child})
	require.NoError(t, err)
	body, err := gateway.Execute(context.Background(), `query { sales: crm { user { id } } }`, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"data":{"sales":null},
		"errors":[{"message":"denied","path":["sales","user"],"locations":[{"line":1,"column":22}],"extensions":{"code":"FORBIDDEN"}}]
	}`, string(body))
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
		"errors":[{"message":"rejected","path":["second"],"locations":[{"line":3,"column":3}]}],
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
		"errors":[{"message":"failed","path":["second"],"locations":[{"line":3,"column":3}]}],
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
	oldEndpoint := newGatewayEndpoint("crm-v1", gatewayChildSchema, func(ctx context.Context, _ string, _ map[string]any) ([]byte, error) {
		close(started)
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
	oldResult := make(chan []byte, 1)
	go func() {
		body, _ := gateway.Execute(context.Background(), `{ crm { user { id } } }`, nil)
		oldResult <- body
	}()
	<-started
	require.NoError(t, gateway.Refresh(Namespace{Name: "crm", Endpoint: newEndpoint}))
	close(release)
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

func newGatewayEndpoint(id, schema string, execute func(context.Context, string, map[string]any) ([]byte, error)) *gatewayEndpoint {
	return &gatewayEndpoint{id: id, snapshot: &Snapshot{schema: schema, fingerprint: id}, execute: execute}
}

func (my *gatewayEndpoint) EndpointID() string           { return my.id }
func (my *gatewayEndpoint) Snapshot() (*Snapshot, error) { return my.snapshot, nil }
func (my *gatewayEndpoint) Execute(ctx context.Context, query string, variables map[string]any) ([]byte, error) {
	my.calls.Add(1)
	return my.execute(ctx, query, variables)
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

	body, err = gateway.Execute(context.Background(), "#"+strings.Repeat("x", gatewayMaxDocumentBytes)+"\n{ crm { user { id } } }", nil)
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
	_, cached := gateway.current().cache.Get(planKey{query: query})
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
	_, cached := gateway.current().cache.Get(planKey{query: query})
	require.False(t, cached)
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
