package gql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Engine.subscribe is a subscription-only entry point.  In particular, a
// rejected query or mutation must not install a CDC feed (or start the first
// SQL fetch in its stream goroutine).
func TestSubscribeRejectsNonSubscriptionBeforeFeedCreation(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()

	for _, tc := range []struct {
		name  string
		query string
	}{
		{name: "query", query: `query { users { total } }`},
		{name: "mutation", query: `mutation { createUser(input: { name: "nope", email: "nope@x.com" }) { id } }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events, err := executor.subscribe(context.Background(), tc.query, nil, "")
			require.Error(t, err, "non-subscription operation must be rejected synchronously")
			require.Nil(t, events)

			executor.feedMu.Lock()
			defer executor.feedMu.Unlock()
			require.Empty(t, executor.feeds, "rejected operation must not create a CDC feed")
		})
	}
}
