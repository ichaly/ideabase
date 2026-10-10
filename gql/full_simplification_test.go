package gql

import (
	"context"
	"strings"
	"testing"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestCloneValueKeepsSharedTemplateImmutable(t *testing.T) {
	leaf := &ast.Value{Kind: ast.StringValue, Raw: "original"}
	member := &ast.ChildValue{Name: "name", Value: leaf}
	template := &ast.Value{Kind: ast.ObjectValue, Children: ast.ChildValueList{member}}
	cloned := cloneValue(template)
	require.Same(t, leaf, member.Value, "cloning must not rewrite shared AST pointers")
	cloned.Children[0].Name = "changed"
	cloned.Children[0].Value.Raw = "changed"
	require.Equal(t, "name", member.Name)
	require.Equal(t, "original", leaf.Raw)
	require.Equal(t, "original", cloneValue(template).Children[0].Value.Raw)
}

func TestProtocolPolicyRetainsAnyInvalidPermission(t *testing.T) {
	permissions := map[string]map[string]*protocol.Permission{"users": {
		"invalid": {Select: []string{"id"}, Filter: []protocol.Predicate{{Column: "id", Operator: "eq", Value: make(chan int)}}},
		"valid":   {Select: []string{"id"}},
	}}
	// Map iteration must never let a valid entry erase an earlier clone error.
	for range 100 {
		provider := NewProtocolPolicyProvider(permissions)
		_, err := provider.Decide(context.Background(), PolicyRequest{Resource: "users"})
		require.ErrorContains(t, err, "invalid protocol policy")
	}
}

func TestMixedRootsKeepSQLResolversAndCodecs(t *testing.T) {
	engine, db, cleanup := newTestExecutor(t, nil, WithCodecs(NewIdCodec()))
	defer cleanup()
	testExec(t, db, `INSERT INTO users(name, email) VALUES ('mixed', 'mixed@example.com')`)
	calls := 0
	require.NoError(t, engine.register(pingResolver(), NewLoader("User", "label", "", "id",
		func(_ context.Context, keys []any) (map[any]string, error) {
			calls++
			require.Len(t, keys, 1)
			_, encoded := keys[0].(string)
			require.False(t, encoded, "loader receives the raw database key")
			return map[any]string{keys[0]: "loaded"}, nil
		})))
	selection := `users { items { id name label } }`
	ordinary := engine.run(context.Background(), "{"+selection+"}", nil, "")
	require.Empty(t, ordinary.Errors)
	require.Equal(t, 1, calls)
	user := ordinary.Data["users"].(map[string]any)["items"].([]any)[0].(map[string]any)
	require.IsType(t, "", user["id"], "ID codec must encode the database value")
	require.Equal(t, "loaded", user["label"])
	for range 2 {
		reply := engine.run(context.Background(), `{ ping(msg:"x") `+selection+` }`, nil, "")
		require.Empty(t, reply.Errors)
		require.Equal(t, ordinary.Data["users"], reply.Data["users"])
		require.Equal(t, "pong:x", reply.Data["ping"])
	}
	require.Equal(t, 3, calls)
}

func TestSDLFieldUsesSharedArguments(t *testing.T) {
	renderer := &Renderer{sb: &strings.Builder{}}
	renderer.writeField("users", "[User!]!", "用户列表", Argument{Name: "limit", Type: "Int", Default: "20"})
	require.Equal(t, "  users(limit: Int = 20): [User!]!  # 用户列表\n", renderer.sb.String())
}

func TestReplyWritersShareNullDataShape(t *testing.T) {
	reply := gqlReply{nullData: true}
	direct, err := reply.MarshalJSON()
	require.NoError(t, err)
	require.JSONEq(t, `{"data":null}`, string(direct))
	bounded, err := marshalReply(WithResponseLimit(context.Background(), 1024), reply)
	require.NoError(t, err)
	require.JSONEq(t, string(direct), string(bounded))
}
