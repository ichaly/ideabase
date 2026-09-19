package gql

import (
	"context"
	"fmt"
	"testing"

	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
)

func TestIntrospectionShowsFullSchemaWithoutGrantingExecution(t *testing.T) {
	builder := New(snapshotKonfig(t), nil, snapshotOptions(snapshotLoader{})...)
	require.NoError(t, builder.Policy(OperationPolicyProvider(func(_ context.Context, request PolicyRequest) (PolicyDecision, error) {
		return PolicyDecision{Allow: request.Coordinate == "Query.greeting"}, nil
	})))
	calls := 0
	require.NoError(t, builder.Register(
		NewResolver("Query", "greeting", "", func(context.Context, Root, struct{}) (string, error) { return "hello", nil }, Access("test", "read")),
		NewResolver("Mutation", "submitTransfer", "", func(context.Context, Root, struct{}) (string, error) { calls++; return "submitted", nil }, Access("test", "write")),
	))
	engine, err := builder.Build()
	require.NoError(t, err)
	t.Cleanup(engine.Close)
	id, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "admin", Roles: []string{"admin"}})
	require.NoError(t, err)
	ctx := WithIdentity(context.Background(), id)
	for _, query := range []string{
		`{ __schema { mutationType { fields { name } } } }`,
		`{ greeting __schema { mutationType { fields { name } } } }`,
		`query($name:String!="Mutation"){ document:__type(name:$name){...Fields} } fragment Fields on __Type { fields { name } }`,
	} {
		body, err := engine.Execute(ctx, query, nil)
		require.NoError(t, err)
		require.NotContains(t, string(body), `"errors"`)
		require.Contains(t, string(body), `"name":"submitTransfer"`)
	}
	for _, query := range []string{`mutation { submitTransfer }`, `{ users { items { name } } }`, `{ users { items { name } } __schema { queryType { name } } }`} {
		body, err := engine.Execute(ctx, query, nil)
		require.NoError(t, err)
		require.Contains(t, string(body), `"errors"`)
	}
	require.Zero(t, calls)
	artifact, err := engine.EffectiveSchema(ctx)
	require.NoError(t, err)
	require.NotContains(t, artifact.Schema, "submitTransfer")
}

func TestIntrospectionConfigurationIgnoresMode(t *testing.T) {
	for _, mode := range []string{"dev", "development", "production"} {
		for _, setting := range []any{nil, false, true} {
			t.Run(fmt.Sprintf("%s/%v", mode, setting), func(t *testing.T) {
				k, err := std.NewKonfig()
				require.NoError(t, err)
				k.Set("mode", mode)
				k.Set("app.root", t.TempDir())
				if setting != nil {
					k.Set("schema.introspection", setting)
				}
				engine, err := New(k, nil, snapshotOptions(snapshotLoader{})...).testAllowAllPolicy().Build()
				require.NoError(t, err)
				t.Cleanup(engine.Close)
				id, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "reader"})
				require.NoError(t, err)
				reply := engine.run(WithIdentity(context.Background(), id), `{ __schema { queryType { name } } }`, nil, "")
				if setting == true {
					require.Empty(t, reply.Errors)
				} else {
					require.NotEmpty(t, reply.Errors)
					require.Equal(t, "INTROSPECTION_DISABLED", reply.Errors[0].Extensions["code"])
				}
			})
		}
	}
	require.False(t, (&Engine{}).options().Introspection)
}
