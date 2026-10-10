package intro

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestIntrospectionReflectsSchemaDirectives(t *testing.T) {
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "schema.graphql", Input: `
		scalar URL @specifiedBy(url: "https://example.test/url")
		input Choice @oneOf {
			old: String @deprecated(reason: "old input")
			current: String
		}
		enum Status {
			OLD @deprecated(reason: "legacy")
			OK
		}
		directive @tag(old: String @deprecated(reason: "old argument")) repeatable on FIELD
		type Query {
			old(arg: String @deprecated(reason: "old argument")): String @deprecated(reason: "old field")
			legacy: String @deprecated
			active: String
			choose(input: Choice): Status
		}
	`})
	require.NoError(t, err)

	query := `query Contract($deprecated: Boolean!) {
		scalar: __type(name: "URL") { specifiedByURL }
		choice: __type(name: "Choice") {
			isOneOf
			inputFields(includeDeprecated: $deprecated) { name isDeprecated deprecationReason }
		}
		status: __type(name: "Status") {
			enumValues(includeDeprecated: $deprecated) { name isDeprecated deprecationReason }
		}
		query: __type(name: "Query") {
			fields(includeDeprecated: $deprecated) {
				name isDeprecated deprecationReason
				args(includeDeprecated: $deprecated) { name isDeprecated deprecationReason }
			}
		}
		__schema {
			directives { name isRepeatable args(includeDeprecated: $deprecated) { name isDeprecated deprecationReason } }
		}
	}`
	doc, errs := gqlparser.LoadQuery(schema, query)
	require.Empty(t, errs)

	without, err := New(schema).Introspect(doc.Operations.ForName("Contract"), map[string]any{"deprecated": false})
	require.NoError(t, err)
	require.Equal(t, "https://example.test/url", without["scalar"].(map[string]any)["specifiedByURL"])
	require.Equal(t, true, without["choice"].(map[string]any)["isOneOf"])
	require.Equal(t, []string{"current"}, names(without["choice"].(map[string]any)["inputFields"]))
	require.Equal(t, []string{"OK"}, names(without["status"].(map[string]any)["enumValues"]))
	require.Equal(t, []string{"active", "choose"}, names(without["query"].(map[string]any)["fields"]))

	with, err := New(schema).Introspect(doc.Operations.ForName("Contract"), map[string]any{"deprecated": true})
	require.NoError(t, err)
	require.Equal(t, []string{"old", "current"}, names(with["choice"].(map[string]any)["inputFields"]))
	require.Equal(t, "old input", item(with["choice"].(map[string]any)["inputFields"], "old")["deprecationReason"])
	require.Equal(t, []string{"OLD", "OK"}, names(with["status"].(map[string]any)["enumValues"]))
	require.Equal(t, "legacy", item(with["status"].(map[string]any)["enumValues"], "OLD")["deprecationReason"])
	require.Equal(t, "old field", item(with["query"].(map[string]any)["fields"], "old")["deprecationReason"])
	require.Equal(t, "No longer supported", item(with["query"].(map[string]any)["fields"], "legacy")["deprecationReason"])
	require.Equal(t, "old argument", item(item(with["query"].(map[string]any)["fields"], "old")["args"], "arg")["deprecationReason"])

	var tag map[string]any
	for _, value := range with["__schema"].(map[string]any)["directives"].([]any) {
		candidate := value.(map[string]any)
		if candidate["name"] == "tag" {
			tag = candidate
			break
		}
	}
	require.NotNil(t, tag)
	require.Equal(t, true, tag["isRepeatable"])
	require.Equal(t, "old argument", item(tag["args"], "old")["deprecationReason"])
}

func names(value any) []string {
	values, _ := value.([]any)
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value.(map[string]any)["name"].(string))
	}
	return out
}

func item(value any, name string) map[string]any {
	for _, value := range value.([]any) {
		item := value.(map[string]any)
		if item["name"] == name {
			return item
		}
	}
	return nil
}
