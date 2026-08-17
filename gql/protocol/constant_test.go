package protocol

import "testing"

func TestProtocolDefaultsAreImmutableCopies(t *testing.T) {
	types := DefaultDataTypes()
	types["integer"] = SCALAR_STRING
	if DefaultDataTypes()["integer"] != SCALAR_INT {
		t.Fatal("database type defaults were globally mutated")
	}
	op, ok := GetOperator(EQ)
	if !ok {
		t.Fatal("eq operator missing")
	}
	op.Value = "FORGED"
	if current, _ := GetOperator(EQ); current.Value != "=" {
		t.Fatal("operator registry was globally mutated")
	}
	group := OperatorsForScalar(SCALAR_INT)
	group[0].Value = "FORGED"
	if OperatorsForScalar(SCALAR_INT)[0].Value == "FORGED" {
		t.Fatal("operator grouping was globally mutated")
	}
}
