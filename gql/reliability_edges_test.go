package gql

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

func TestSafelyHidesPanicDetails(t *testing.T) {
	err := safely(func() error { panic("secret panic value") })
	require.Error(t, err)
	require.Equal(t, "RESOLVER_ERROR", errorCode(err))
	require.NotContains(t, err.Error(), "secret panic value")
	require.NotContains(t, err.Error(), "goroutine")
}

func errorCode(err error) string {
	var ge *gqlerror.Error
	if errors.As(err, &ge) {
		return ge.Extensions["code"].(string)
	}
	return ""
}

func TestSubscriptionTableIdentityKeepsSchema(t *testing.T) {
	require.Equal(t, "tenant_a.users", subscriptionTableIdentity("tenant_a", "users"))
	require.Equal(t, "tenant_a.users", subscriptionTableIdentity("tenant_a", "tenant_a.users"))
}

func TestDispatchUnknownRelationDoesNotTouchEmptyTable(t *testing.T) {
	l := newListener("", "")
	relations := map[uint32]string{}
	touched := map[string]bool{}
	// A malformed/unknown relation id must not create the empty table key.
	l.dispatch([]byte{0}, relations, touched)
	require.NotContains(t, touched, "")
}

func TestSocketSessionHasActiveSubscriptionLimit(t *testing.T) {
	s := &socketSession{subs: make(map[string]context.CancelFunc)}
	for i := 0; i < maxActiveSubscriptions; i++ {
		require.True(t, s.reserve(fmt.Sprint(i)))
	}
	require.False(t, s.reserve("overflow"))
}
