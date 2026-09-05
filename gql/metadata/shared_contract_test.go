package metadata

import (
	"encoding/json"
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/protocol"
)

func TestNullableTypeAcceptsWireRepresentations(t *testing.T) {
	for _, input := range []string{`true`, `1`, `"true"`} {
		var value NullableType
		if err := json.Unmarshal([]byte(input), &value); err != nil || !value.Bool() {
			t.Fatalf("input %s -> %v, %v", input, value, err)
		}
	}
	for _, input := range []string{`false`, `0`, `"0"`} {
		var value NullableType
		if err := json.Unmarshal([]byte(input), &value); err != nil || value.Bool() {
			t.Fatalf("input %s -> %v, %v", input, value, err)
		}
	}
	var value NullableType
	if err := json.Unmarshal([]byte(`null`), &value); err == nil {
		t.Fatal("null nullable value should be rejected")
	}
}

func TestMetadataNameConversionAndPredicateValidation(t *testing.T) {
	config := internal.MetadataConfig{TablePrefix: []string{"sys_"}, UseSingular: true, UseCamel: true}
	if got := ConvertClassName("sys_users", config); got != "User" {
		t.Fatalf("class name = %q", got)
	}
	if got := ConvertFieldName("created_at", config); got != "createdAt" {
		t.Fatalf("field name = %q", got)
	}
	loader := NewConfigLoader(&internal.Config{})
	class := &protocol.Class{Fields: map[string]*protocol.Field{"id": {Name: "id", Column: "id"}}}
	for _, predicate := range []protocol.Predicate{{Operator: "eq", Column: "id", Context: "tenant"}, {Operator: "is-null", Column: "id"}} {
		if _, err := loader.resolvePredicate(class, predicate); err != nil {
			t.Fatalf("predicate %#v rejected: %v", predicate, err)
		}
	}
	for _, predicate := range []protocol.Predicate{{Operator: "eq", Column: "id"}, {Operator: "is-null", Column: "id", Value: true}, {Operator: "bad", Column: "id"}} {
		if _, err := loader.resolvePredicate(class, predicate); err == nil {
			t.Fatalf("predicate %#v unexpectedly accepted", predicate)
		}
	}
}
