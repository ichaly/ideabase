package metadata

import (
	"encoding/json"
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func governanceConfig() *internal.Config {
	return &internal.Config{Metadata: internal.MetadataConfig{UseCamel: true, UseSingular: true, Classes: map[string]*internal.ClassConfig{
		"User": {Table: "users", Operations: []string{"select"}, SoftDelete: &protocol.SoftDelete{Column: "deleted_at"},
			Where:       []protocol.Predicate{{Column: "tenantId", Operator: "eq", Context: "tenant"}},
			Permissions: map[string]*protocol.Permission{"admin": {Select: []string{"id", "display_name"}, Delete: true, Filter: []protocol.Predicate{{Column: "deleted_at", Operator: "is-null"}}}},
			Fields: map[string]*internal.FieldConfig{
				"id":          {Column: "id", IsPrimary: true, ReadOnly: true},
				"displayName": {Column: "display_name", InsertPreset: &protocol.ValueSource{Context: "name"}},
				"tenantId":    {Column: "tenant_id"}, "deletedAt": {Column: "deleted_at"},
			},
		},
	}}}
}

func TestConfigLoaderGovernanceResolvesAndMerges(t *testing.T) {
	h := newFakeHoster()
	require.NoError(t, h.PutNode("users", &protocol.Class{Name: "users", Table: "users", Fields: map[string]*protocol.Field{
		"id": {Name: "id", Column: "id"}, "display_name": {Name: "display_name", Column: "display_name"},
		"tenant_id": {Name: "tenantId", Column: "tenant_id"}, "deleted_at": {Name: "deletedAt", Column: "deleted_at"},
	}}))
	require.NoError(t, NewConfigLoader(governanceConfig()).Load(h))
	c, ok := h.GetNode("users")
	require.True(t, ok)
	assert.Equal(t, []string{"select"}, c.Operations)
	assert.Equal(t, "deleted_at", c.SoftDelete.Column)
	assert.Contains(t, c.Where, protocol.Predicate{Column: "tenant_id", Operator: "eq", Context: "tenant"})
	assert.Equal(t, []string{"id", "displayName"}, c.Permissions["admin"].Select)
	assert.Equal(t, "deleted_at", c.Permissions["admin"].Filter[0].Column)
	assert.True(t, c.Fields["id"].ReadOnly)
	assert.Equal(t, "name", c.Fields["displayName"].InsertPreset.Context)
}

func TestConfigLoaderGovernanceRejectsInvalidDefinitions(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*internal.ClassConfig)
	}{
		{"unknown column", func(c *internal.ClassConfig) {
			c.Where = []protocol.Predicate{{Column: "missing", Operator: "eq", Value: "x"}}
		}},
		{"invalid operator", func(c *internal.ClassConfig) {
			c.Where = []protocol.Predicate{{Column: "id", Operator: "like", Value: "x"}}
		}},
		{"context and value", func(c *internal.ClassConfig) {
			c.Where = []protocol.Predicate{{Column: "id", Operator: "eq", Context: "id", Value: "x"}}
		}},
		{"null with value", func(c *internal.ClassConfig) {
			c.Where = []protocol.Predicate{{Column: "id", Operator: "is-null", Value: true}}
		}},
		{"empty permission", func(c *internal.ClassConfig) {
			c.Permissions = map[string]*protocol.Permission{"admin": nil}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := governanceConfig()
			tc.mutate(cfg.Metadata.Classes["User"])
			h := newFakeHoster()
			h.PutNode("users", &protocol.Class{Name: "users", Table: "users", Fields: map[string]*protocol.Field{"id": {Name: "id", Column: "id"}, "tenant_id": {Name: "tenantId", Column: "tenant_id"}, "deleted_at": {Name: "deletedAt", Column: "deleted_at"}, "display_name": {Name: "displayName", Column: "display_name"}}})
			assert.Error(t, NewConfigLoader(cfg).Load(h))
		})
	}
}

func TestSamePredicatePreservesLiteralTypes(t *testing.T) {
	assert.False(t, samePredicate(
		protocol.Predicate{Column: "id", Operator: "eq", Value: 1},
		protocol.Predicate{Column: "id", Operator: "eq", Value: "1"},
	))
}

func TestGovernanceJSONRoundTrip(t *testing.T) {
	c := &protocol.Class{Name: "User", Table: "users", Operations: []string{"select"}, SoftDelete: &protocol.SoftDelete{Column: "deleted_at"},
		Where: []protocol.Predicate{{Column: "tenant_id", Operator: "eq", Context: "tenant"}}, Permissions: map[string]*protocol.Permission{"admin": {Select: []string{"id"}, Delete: true}},
		Fields: map[string]*protocol.Field{"id": {Name: "id", Column: "id", ReadOnly: true, InsertPreset: &protocol.ValueSource{Value: "x"}}}}
	b, err := json.Marshal(c)
	require.NoError(t, err)
	var got protocol.Class
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, c.Operations, got.Operations)
	assert.Equal(t, c.SoftDelete, got.SoftDelete)
	assert.Equal(t, c.Where, got.Where)
	assert.Equal(t, c.Permissions, got.Permissions)
	assert.Equal(t, c.Fields["id"].InsertPreset.Value, got.Fields["id"].InsertPreset.Value)
}
