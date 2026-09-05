package protocol

import "testing"

func TestClassFieldRelationAndInsertHelpers(t *testing.T) {
	if (*Class)(nil).CanInsertWithoutInput() {
		t.Fatal("nil class should not be insertable")
	}
	class := &Class{Name: "User", Fields: map[string]*Field{}}
	field := &Field{Name: "id", Column: "id", IsPrimary: true, Catalog: &Column{Default: &ColumnDefault{Expression: "nextval"}}}
	class.AddField(field)
	alias := &Field{Name: "displayName", Column: "display_name", Catalog: &Column{Default: &ColumnDefault{Expression: "''"}}}
	class.AddField(alias)
	relation := &Relation{Name: "posts", SourceClass: "User", TargetClass: "Post"}
	class.AddRelation(relation)
	class.AddRelation(relation.Clone(relation.Type, false))
	if class.Fields["id"] != field || class.Fields["display_name"] != alias || len(class.Relations) != 1 {
		t.Fatal("class helpers did not register metadata")
	}
	if !class.CanInsertWithoutInput() {
		t.Fatal("generated-only class should allow default insert")
	}
	class.Fields["name"] = &Field{Name: "name", Column: "name"}
	if class.CanInsertWithoutInput() {
		t.Fatal("writable class should require input")
	}
}

func TestClassConflictTargetLookups(t *testing.T) {
	class := &Class{PrimaryKeys: []string{"id"}, Constraints: []*Constraint{{Name: "users_pkey", Kind: ConstraintPrimary, Columns: []string{"id"}}}}
	if _, ok := class.ConflictTarget("missing"); ok {
		t.Fatal("missing conflict target resolved")
	}
	if _, ok := class.UpsertConflictTarget("PRIMARY"); !ok {
		t.Fatal("primary upsert conflict target missing")
	}
	if _, ok := class.UpsertConflictTarget("PRIMARY"); !ok {
		t.Fatal("named upsert conflict target missing")
	}
}
