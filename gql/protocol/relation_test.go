package protocol

import "testing"

func TestRelationColumnGroupsAreDetached(t *testing.T) {
	relation := &Relation{SourceFields: []string{"tenant_id", "user_id"}, TargetFields: []string{"tenant_id", "id"}}
	source, target := relation.SourceColumns(), relation.TargetColumns()
	source[0], target[0] = "changed", "changed"
	if relation.SourceFields[0] != "tenant_id" || relation.TargetFields[0] != "tenant_id" {
		t.Fatal("column accessors exposed mutable relation metadata")
	}
	clone := relation.Clone(MANY_TO_ONE, false)
	clone.SourceFields[0], clone.TargetFields[0] = "clone", "clone"
	if relation.SourceFields[0] != "tenant_id" || relation.TargetFields[0] != "tenant_id" {
		t.Fatal("relation clone shares mutable column metadata")
	}
}
