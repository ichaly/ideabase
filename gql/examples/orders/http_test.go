package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/ichaly/ideabase/std"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/transport"
	"github.com/stretchr/testify/require"
)

var testSecret = []byte("orders-integration-test-key-32-bytes")

func token(t *testing.T, tenant, role string, expires time.Time) string {
	t.Helper()
	value, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": "orders-auth", "aud": "orders-api", "sub": "test-user", "tenant": tenant, "roles": []string{role}, "exp": expires.Unix(),
	}).SignedString(testSecret)
	require.NoError(t, err)
	return value
}
func TestOrderHTTPAuthentication(t *testing.T) {
	e, _ := testEndpoint(t)
	h, err := newHTTPHandler(e, testSecret)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, token string
		status      int
	}{
		{"missing", "", 401}, {"malformed", "invalid", 401},
		{"expired", token(t, "a", "operator", time.Now().Add(-time.Minute)), 401},
		{"signed", token(t, "a", "viewer", time.Now().Add(time.Hour)), 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/graphql", strings.NewReader(`{"query":"{ products { items { name stock } } }"}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+tc.token)
			r.Header.Set("X-Demo-Tenant", "b")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			if tc.status == http.StatusOK {
				var reply map[string]any
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &reply))
				require.Empty(t, reply["errors"])
				items := reply["data"].(map[string]any)["products"].(map[string]any)["items"].([]any)
				require.Len(t, items, 2, "client header must not override signed tenant")
			}
		})
	}
}

func TestOrderRemoteComposition(t *testing.T) {
	e, pool := testEndpoint(t)
	db, err := std.WrapConnection(pool, "postgres")
	require.NoError(t, err)
	replay, err := gql.NewPostgresReplayStore(db)
	require.NoError(t, err)
	codec, err := gql.NewDelegationCodec(gql.DelegationConfig{Issuer: "admin", Audience: "orders", Key: testSecret, Replay: replay})
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle("/manifest", transport.NewManifestHandler("orders", e, func(*http.Request) bool { return true })) // Loopback test server only.
	mux.Handle("/effective-schema", transport.NewEffectiveSchemaHandler(e, codec))
	mux.Handle("/graphql", transport.NewHandler(e, transport.Options{Delegation: codec}))
	mux.Handle("/health", transport.NewHealthHandler(e))
	server := httptest.NewServer(mux)
	defer server.Close()
	remote, err := transport.NewRemoteEndpoint(context.Background(), transport.RemoteOptions{
		EndpointID: "orders", ManifestURL: server.URL + "/manifest", EffectiveSchemaURL: server.URL + "/effective-schema",
		ExecuteURL: server.URL + "/graphql", SubscribeURL: server.URL + "/graphql", HealthURL: server.URL + "/health", Delegation: codec, AllowHTTP: true,
	})
	require.NoError(t, err)
	composed, err := gql.NewGateway("admin", gql.Namespace{Name: "sales", EndpointID: "orders", Endpoint: remote})
	require.NoError(t, err)
	reply := execute(t, composed, "a", `mutation { sales_placeOrder(requestId:"remote",customerId:1,items:[{productId:1,quantity:1}]) { id status } }`)
	require.Empty(t, reply["errors"], "%v", reply)
	reply = execute(t, composed, "b", `{ sales { orders { total } } }`)
	require.Empty(t, reply["errors"], "%v", reply)
	require.EqualValues(t, 0, reply["data"].(map[string]any)["sales"].(map[string]any)["orders"].(map[string]any)["total"])
}

func TestOrderSSEEndsWhenAccessTokenExpires(t *testing.T) {
	e, _ := testEndpoint(t)
	h, err := newHTTPHandler(e, testSecret)
	require.NoError(t, err)
	server := httptest.NewServer(h)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/graphql", strings.NewReader(`{"query":"subscription { products { items { stock } } }"}`))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Authorization", "Bearer "+token(t, "a", "viewer", time.Now().Add(2*time.Second)))
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, 200, response.StatusCode)
	scanner := bufio.NewScanner(response.Body)
	received := false
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data: ") {
			received = true
			break
		}
	}
	require.NoError(t, scanner.Err())
	require.True(t, received, "valid token must receive an initial subscription event")
	done := make(chan struct{})
	go func() { io.Copy(io.Discard, response.Body); close(done) }()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("expired JWT left SSE stream active")
	}
}

// Execute the tutorial's literal requests, so documentation cannot drift from the API.
func TestOrderTutorialRequests(t *testing.T) {
	e, _ := testEndpoint(t)
	h, err := newHTTPHandler(e, testSecret)
	require.NoError(t, err)
	document, err := os.ReadFile("../../docs/tutorial/02-first-query.md")
	require.NoError(t, err)
	blocks := regexp.MustCompile("(?s)```json\\n(.*?)\\n```").FindAllSubmatch(document, -1)
	require.Len(t, blocks, 6)
	for i, block := range blocks {
		t.Run(fmt.Sprint(i+1), func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(block[1]))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+token(t, "a", "operator", time.Now().Add(time.Hour)))
			response := httptest.NewRecorder()
			h.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var result map[string]any
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
			require.Empty(t, result["errors"], response.Body.String())
			require.NotEmpty(t, result["data"], response.Body.String())
			data := result["data"].(map[string]any)
			switch i {
			case 0:
				require.EqualValues(t, 1, data["customers"].(map[string]any)["total"])
			case 1:
				require.Len(t, data["customers"].(map[string]any)["items"], 1)
			case 2:
				require.Equal(t, "Tutorial Customer", data["createCustomer"].(map[string]any)["name"])
			case 3:
				require.Equal(t, "Alice Updated", data["updateCustomer"].(map[string]any)["name"])
			case 4:
				require.Equal(t, "placed", data["placeOrder"].(map[string]any)["status"])
			case 5:
				require.EqualValues(t, 1, data["orders"].(map[string]any)["total"])
			}

		})
	}
}
