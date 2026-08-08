package gql

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSchemaProfileCompatibilityDetectsClientBreakingChanges(t *testing.T) {
	previous := SchemaProfile{Name: "v1", SchemaArtifact: SchemaArtifact{Schema: `
		type Query { users(limit: Int): [User!]! }
		type User { id: ID!, role: Role! }
		input Filter { name: String }
		enum Role { ADMIN USER }
	`}}
	tests := map[string]string{
		"removed enum value":       `type Query { users(limit: Int): [User!]! } type User { id: ID!, role: Role! } enum Role { USER }`,
		"removed argument":         `type Query { users: [User!]! } type User { id: ID!, role: Role! } enum Role { ADMIN USER }`,
		"changed argument":         `type Query { users(limit: Float): [User!]! } type User { id: ID!, role: Role! } enum Role { ADMIN USER }`,
		"new required argument":    `type Query { users(limit: Int, tenant: ID!): [User!]! } type User { id: ID!, role: Role! } enum Role { ADMIN USER }`,
		"new required input field": `type Query { users(limit: Int, filter: Filter): [User!]! } type User { id: ID!, role: Role! } input Filter { name: String, tenant: ID! } enum Role { ADMIN USER }`,
	}
	for name, schema := range tests {
		t.Run(name, func(t *testing.T) {
			err := CheckProfileCompatibility(previous, SchemaProfile{Name: "v2", SchemaArtifact: SchemaArtifact{Schema: schema}})
			require.ErrorContains(t, err, "incompatible schema profile")
		})
	}
}
