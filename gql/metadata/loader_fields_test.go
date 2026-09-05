package metadata

import (
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/protocol"
)

func TestConfigLoaderFieldFilteringAndRelationConstruction(t *testing.T) {
	loader := NewConfigLoader(&internal.Config{Metadata: internal.MetadataConfig{UseCamel: true}})
	class := &protocol.Class{Name: "User", Fields: map[string]*protocol.Field{
		"id": {Name: "id", Column: "id"}, "display_name": {Name: "display_name", Column: "display_name"}, "hidden": {Name: "hidden", Column: "hidden"},
	}}
	loader.applyFieldFilter(class, &internal.ClassConfig{IncludeFields: []string{"id", "display_name", "missing"}, ExcludeFields: []string{"display_name"}})
	if len(class.Fields) != 1 || class.Fields["id"] == nil {
		t.Fatalf("filtered fields = %#v", class.Fields)
	}
	field := loader.buildFieldFromConfig("User", "posts", "post_id", &internal.FieldConfig{
		Type: "ID", Description: "posts", IsUnique: true, DefaultValue: "0",
		InsertPreset: &protocol.ValueSource{Context: "tenant"}, UpdatePreset: &protocol.ValueSource{Value: "x"},
		Relation: &internal.RelationConfig{TargetClass: "Post", TargetField: "id", Type: string(protocol.ONE_TO_MANY), SourceFields: []string{"post_id"}, TargetFields: []string{"id"}, Through: &internal.ThroughConfig{TableName: "user_posts", SourceKey: "user_id", TargetKey: "post_id"}},
	}, nil)
	if field.Relation == nil || field.Relation.TargetClass != "Post" || field.Relation.Through == nil || field.Catalog == nil || field.InsertPreset == nil || field.UpdatePreset == nil {
		t.Fatalf("configured field was not built: %#v", field)
	}
}
