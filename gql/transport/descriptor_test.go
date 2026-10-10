package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTransportDescriptorRejectsContractDrift(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(TransportDescriptor{Version: TransportDescriptorVersion, EndpointID: "child", ManifestURL: "/manifest", ExecuteURL: "/graphql", DelegationScheme: "Bearer", SnapshotHeader: "GQL-Snapshot", ExecutionPinHeader: executionPinHeader, ResponseMediaType: responseType})
	}))
	defer server.Close()
	_, err := DiscoverRemoteEndpoint(context.Background(), server.URL, RemoteOptions{AllowHTTP: true})
	if err == nil {
		t.Fatal("descriptor contract drift was accepted")
	}
}

func TestTransportDescriptorNon200DecodesRemoteProblem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusServiceUnavailable, "DESCRIPTOR_BUSY", "try later", true)
	}))
	defer server.Close()
	_, err := DiscoverRemoteEndpoint(context.Background(), server.URL, RemoteOptions{AllowHTTP: true})
	var problem *RemoteProblemError
	if !errors.As(err, &problem) || problem.Code != "DESCRIPTOR_BUSY" {
		t.Fatalf("descriptor error = %v", err)
	}
}

func TestTransportDescriptorDiscoversRemoteEndpoint(t *testing.T) {
	snapshot := newArtifact(t, "type Query { hello: String }")
	endpoint := &runtimeEndpoint{snapshot: snapshot, body: []byte(`{"data":{"hello":"world"}}`)}
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	mux.Handle("/manifest", testManifestHandler("child", endpoint))
	mux.Handle("/graphql", NewHandler(endpoint, Options{}))
	mux.Handle("/.well-known/gql-gateway", NewTransportDescriptorHandler(TransportDescriptor{
		EndpointID: "child", ManifestURL: "/manifest", ExecuteURL: "/graphql",
	}))

	remote, err := DiscoverRemoteEndpoint(context.Background(), server.URL+"/.well-known/gql-gateway", RemoteOptions{AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	body, err := remote.Execute(context.Background(), `{ hello }`, nil)
	if err != nil || string(body) != `{"data":{"hello":"world"}}` {
		t.Fatalf("execute = %s, %v", body, err)
	}
}

func TestTransportDescriptorRejectsUnknownVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":99,"endpointId":"child","manifest":"/manifest","execute":"/graphql"}`))
	}))
	defer server.Close()
	_, err := DiscoverRemoteEndpoint(context.Background(), server.URL, RemoteOptions{AllowHTTP: true})
	if err == nil {
		t.Fatal("unknown descriptor version was accepted")
	}
}

func TestTransportDescriptorRejectsCrossOriginEndpoints(t *testing.T) {
	target := httptest.NewTLSServer(http.NotFoundHandler())
	defer target.Close()
	descriptor := httptest.NewTLSServer(NewTransportDescriptorHandler(TransportDescriptor{
		EndpointID: "child", ManifestURL: target.URL + "/manifest", ExecuteURL: target.URL + "/graphql",
	}))
	defer descriptor.Close()
	_, err := DiscoverRemoteEndpoint(context.Background(), descriptor.URL, RemoteOptions{Client: descriptor.Client()})
	if err == nil {
		t.Fatal("cross-origin descriptor endpoint was accepted")
	}
}
