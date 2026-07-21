package gql

import (
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

// graphql-transport-ws 协议状态机:未init即subscribe须关4401、重复init关4429、
// init超时关4408、重复订阅ID关4409——缺状态机时鉴权握手可被绕过
func TestSocketProtocolGuards(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()

	app := fiber.New()
	executor.Bind(app.Group(executor.Path()))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true}) }()
	defer func() { _ = app.Shutdown() }()
	url := "ws://" + ln.Addr().String() + executor.Path()

	dial := func(t *testing.T) *websocket.Conn {
		header := http.Header{"Sec-WebSocket-Protocol": []string{"graphql-transport-ws"}}
		var conn *websocket.Conn
		require.Eventually(t, func() bool { // 等待监听就绪
			c, _, err := websocket.DefaultDialer.Dial(url, header)
			conn = c
			return err == nil
		}, 3*time.Second, 50*time.Millisecond)
		return conn
	}
	expectClose := func(t *testing.T, conn *websocket.Conn, code int) {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			var frame map[string]any
			if err := conn.ReadJSON(&frame); err != nil {
				closeErr, ok := err.(*websocket.CloseError)
				require.True(t, ok, "应收到close帧, 实际: %v", err)
				require.Equal(t, code, closeErr.Code)
				return
			}
		}
	}
	writeJSON := func(t *testing.T, conn *websocket.Conn, v map[string]any) {
		require.NoError(t, conn.WriteJSON(v))
	}
	subscribe := map[string]any{"id": "1", "type": "subscribe",
		"payload": map[string]any{"query": "subscription { users { total } }"}}

	t.Run("未init即subscribe关4401", func(t *testing.T) {
		conn := dial(t)
		defer conn.Close()
		writeJSON(t, conn, subscribe)
		expectClose(t, conn, 4401)
	})

	t.Run("重复init关4429", func(t *testing.T) {
		conn := dial(t)
		defer conn.Close()
		writeJSON(t, conn, map[string]any{"type": "connection_init"})
		writeJSON(t, conn, map[string]any{"type": "connection_init"})
		expectClose(t, conn, 4429)
	})

	t.Run("init超时关4408", func(t *testing.T) {
		old := wsInitTimeout
		wsInitTimeout = 200 * time.Millisecond
		defer func() { wsInitTimeout = old }()
		conn := dial(t)
		defer conn.Close()
		expectClose(t, conn, 4408)
	})

	t.Run("重复订阅ID关4409", func(t *testing.T) {
		conn := dial(t)
		defer conn.Close()
		writeJSON(t, conn, map[string]any{"type": "connection_init"})
		var ack map[string]any
		require.NoError(t, conn.ReadJSON(&ack))
		require.Equal(t, "connection_ack", ack["type"])
		writeJSON(t, conn, subscribe)
		writeJSON(t, conn, subscribe)
		expectClose(t, conn, 4409)
	})
}
