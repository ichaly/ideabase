package gql

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

// Executor.subscribe is a subscription-only entry point.  In particular, a
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

// The HTTP request context is unavailable after a WebSocket upgrade.  The
// authenticated identity and the route selected for it therefore have to be
// carried into the socket session along with row scope.  The revocation
// barrier makes both values observable without relying on application data.
func TestSubscriptionSocketPreservesIdentityAndPinnedRoute(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	executor.revocation = NewRevocationBarrier(1, map[string]TenantSecurityState{
		"tenant-a": {MinimumPolicyRevision: 1, MinimumRouteRevision: 1},
	})

	identity, err := NewIdentity(IdentityInput{Tenant: "tenant-a", Subject: "user-a", PolicyRevision: 1})
	require.NoError(t, err)
	route := TenantRoute{Tenant: "tenant-a", Revision: 1, CacheKey: "pinned-route"}
	authenticated := PinRoute(WithIdentity(context.Background(), identity), route)

	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.SetContext(authenticated)
		return c.Next()
	})
	executor.Bind(app.Group(executor.Path()))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	defer func() { _ = app.Shutdown() }()
	url := "ws://" + ln.Addr().String() + executor.Path()

	var conn *websocket.Conn
	header := http.Header{"Sec-WebSocket-Protocol": []string{"graphql-transport-ws"}}
	require.Eventually(t, func() bool {
		conn, _, err = websocket.DefaultDialer.Dial(url, header)
		return err == nil
	}, 3*time.Second, 50*time.Millisecond)
	defer conn.Close()

	require.NoError(t, conn.WriteJSON(map[string]any{"type": "connection_init"}))
	var ack map[string]any
	require.NoError(t, conn.ReadJSON(&ack))
	require.Equal(t, "connection_ack", ack["type"])
	require.NoError(t, conn.WriteJSON(map[string]any{
		"id": "1", "type": "subscribe",
		"payload": map[string]any{"query": `subscription { users { total } }`},
	}))

	var next map[string]any
	require.NoError(t, conn.ReadJSON(&next))
	require.Equal(t, "next", next["type"], "subscription should retain verified identity and pinned route")
	payload, ok := next["payload"].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, payload, "errors")
	require.NoError(t, conn.WriteJSON(map[string]any{"id": "1", "type": "complete"}))
	var complete map[string]any
	require.NoError(t, conn.ReadJSON(&complete))
	require.Equal(t, "complete", complete["type"])
}
