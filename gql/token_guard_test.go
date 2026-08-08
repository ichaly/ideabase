package gql

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
	validatorrules "github.com/vektah/gqlparser/v2/validator/rules"
)

func TestTokenLimitExactBoundary(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	const query = `{ users { total } }`
	rules := validatorrules.NewDefaultRules()
	boundary := 0
	for limit := 1; limit < 32; limit++ {
		if _, errs := loadQuery(executor.schema, query, limit, rules); len(errs) == 0 {
			boundary = limit
			break
		}
	}
	require.Positive(t, boundary)
	_, below := loadQuery(executor.schema, query, boundary-1, rules)
	require.NotEmpty(t, below)
	require.Contains(t, below.Error(), "token limit")
	_, exact := loadQuery(executor.schema, query, boundary, rules)
	require.Empty(t, exact)
}

func TestTokenLimitRejectsBeforeResolverAndPersistedRegistration(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, func(k *std.Konfig) {
		k.Set("schema.max-tokens", 3)
	})
	defer cleanup()
	var calls atomic.Int32
	require.NoError(t, executor.Register(NewResolver[Root, struct{}, string]("Query", "weighted", "", func(context.Context, Root, struct{}) (string, error) {
		calls.Add(1)
		return "ok", nil
	})))

	boundary := executor.run(context.Background(), `{ weighted }`, nil, "")
	require.Empty(t, boundary.Errors)
	require.EqualValues(t, 1, calls.Load())
	over := executor.run(context.Background(), `{ alias: weighted }`, nil, "")
	require.NotEmpty(t, over.Errors)
	require.Contains(t, over.Errors[0].Message, "token limit")
	require.EqualValues(t, 1, calls.Load())

	err := executor.loadDocument(`query TooLarge { weighted }`)
	require.Error(t, err)
	_, exists := executor.documents["TooLarge"]
	require.False(t, exists)
}

func TestDefaultTokenLimitAcceptsStandardIntrospectionQuery(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	reply := executor.run(context.Background(), standardIntrospectionQuery, nil, "IntrospectionQuery")
	require.Empty(t, reply.Errors)
	require.Contains(t, reply.Data, "__schema")
}
