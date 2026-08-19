package protocol

import "testing"

func TestProtocolScalarAndOperatorCatalog(t *testing.T) {
	for _, scalar := range ScalarTypes() {
		if !IsBuiltinScalar(scalar) || len(OperatorsForScalar(scalar)) == 0 {
			t.Fatalf("scalar %q has no operator catalog", scalar)
		}
	}
	if _, ok := GetOperator("missing"); ok {
		t.Fatal("unknown operator unexpectedly resolved")
	}
}

func TestProtocolConstraintAndIndexPredicates(t *testing.T) {
	if (&Constraint{Kind: ConstraintPrimary, Columns: []string{"id"}}).IsConflictTarget() != true {
		t.Fatal("primary key should be a conflict target")
	}
	for _, constraint := range []*Constraint{nil, {}, {Kind: ConstraintUnique, Deferrable: true}, {Kind: ConstraintCheck, Columns: []string{"x"}}} {
		if constraint != nil && constraint.IsKey() && constraint.IsConflictTarget() {
			t.Fatalf("constraint %#v should not be usable", constraint)
		}
	}
	for _, index := range []*Index{nil, {}, {Unique: true, Invalid: true, Keys: []IndexKey{{Column: "id"}}}, {Unique: true, Keys: []IndexKey{{Expression: "lower(name)"}}}} {
		if index != nil && index.IsUsableConflictTarget() {
			t.Fatalf("index %#v should not be usable", index)
		}
	}
	if !(&Index{Unique: true, Keys: []IndexKey{{Column: "id"}}}).IsUsableConflictTarget() {
		t.Fatal("valid unique index was rejected")
	}
}

func TestProtocolTableAndRelationHelpers(t *testing.T) {
	if !TableView.ReadOnly() || !TableMaterialized.ReadOnly() || !TableForeign.ReadOnly() || TableBase.ReadOnly() {
		t.Fatal("table read-only classification is incorrect")
	}
	relation := (&Relation{SourceFields: []string{"id"}, TargetFields: []string{"user_id"}}).Clone(MANY_TO_ONE, true)
	if relation.Type != MANY_TO_ONE || relation.Key() == "" || relation.Composite() {
		t.Fatal("relation clone/key classification is incorrect")
	}
	if (&RemoteRef{Key: "id"}).Alias() != "__rk_id" {
		t.Fatal("remote reference alias is incorrect")
	}
}
