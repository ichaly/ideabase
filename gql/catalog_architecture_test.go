package gql

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewSnapshotArtifactRejectsForgedAdvertisedFingerprint(t *testing.T) {
	const schema = "type Query { hello: String }"

	_, err := NewSnapshotArtifact(schema, "forged-fingerprint", []Capability{
		{
			Coordinate: "Query.hello",
			Parent:     "Query",
			Name:       "hello",
			Type:       "String",
			Handler:    HandlerRemote,
		},
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "fingerprint")
}

func TestNewSnapshotArtifactRejectsExecutableRootWithoutCapability(t *testing.T) {
	_, err := NewSnapshotArtifact("type Query { hello: String }", "revision", nil)

	require.Error(t, err)
	require.ErrorContains(t, err, "capability")
}
