package compiler

import (
	"encoding/base64"
	"testing"
)

func TestCursorTokenBindsContractAndTuple(t *testing.T) {
	contract := CursorContract{StructuralFingerprint: "shape-a", PolicyRevision: 7, Tenant: "tenant-a", Route: "route-3", Coordinate: "Query.users", Sort: []string{"name:ASC", "id:ASC"}}
	token, err := EncodeCursorToken(contract, []any{"Bob", int64(2)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeCursorToken(token, contract)
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "Bob" || got[1] != int64(2) {
		t.Fatalf("unexpected tuple: %#v", got)
	}

	for name, other := range map[string]CursorContract{
		"structural fingerprint": {StructuralFingerprint: "shape-b", PolicyRevision: 7, Tenant: "tenant-a", Route: "route-3", Coordinate: "Query.users", Sort: contract.Sort},
		"policy revision":        {StructuralFingerprint: "shape-a", PolicyRevision: 8, Tenant: "tenant-a", Route: "route-3", Coordinate: "Query.users", Sort: contract.Sort},
		"tenant":                 {StructuralFingerprint: "shape-a", PolicyRevision: 7, Tenant: "tenant-b", Route: "route-3", Coordinate: "Query.users", Sort: contract.Sort},
		"coordinate":             {StructuralFingerprint: "shape-a", PolicyRevision: 7, Tenant: "tenant-a", Route: "route-3", Coordinate: "Query.posts", Sort: contract.Sort},
		"sort":                   {StructuralFingerprint: "shape-a", PolicyRevision: 7, Tenant: "tenant-a", Route: "route-3", Coordinate: "Query.users", Sort: []string{"id:ASC"}},
	} {
		if _, err := DecodeCursorToken(token, other); err == nil {
			t.Errorf("%s: expected contract mismatch", name)
		}
	}
}

func TestResolveSlotsRejectsMalformedOrCrossContractCursor(t *testing.T) {
	contract := CursorContract{StructuralFingerprint: "shape", PolicyRevision: 2, Tenant: "t", Route: "r", Coordinate: "Query.users", Sort: []string{"id:ASC"}}
	token, err := EncodeCursorToken(contract, []any{int64(4)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := NewContext(nil, `"`, map[string]interface{}{"c": token})
	ctx.SetCursorContract(contract)
	ctx.AddCursor("c", 0)
	if _, err := ctx.Args(); err != nil {
		t.Fatal(err)
	}
	ctx.Release()

	ctx = NewContext(nil, `"`, map[string]interface{}{"c": token + "x"})
	ctx.SetCursorContract(contract)
	ctx.AddCursor("c", 0)
	if _, err := ctx.Args(); err == nil {
		t.Fatal("tampered cursor should fail before SQL")
	}
	ctx.Release()
}

// 雪花ID等bigint超过2^53后经float64解码必失真,游标续页会重复或漏行
func TestDecodeCursorBigintPrecision(t *testing.T) {
	cursor := base64.StdEncoding.EncodeToString([]byte(`[9007199254740993, "abc", 3.14]`))
	keys, err := DecodeCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := keys[0].(int64); !ok || got != 9007199254740993 {
		t.Fatalf("整数键应精确还原为int64(9007199254740993), 实际 %T(%v)", keys[0], keys[0])
	}
	if keys[1] != "abc" {
		t.Fatalf("字符串键失真: %v", keys[1])
	}
	if got, ok := keys[2].(float64); !ok || got != 3.14 {
		t.Fatalf("小数键应还原为float64(3.14), 实际 %T(%v)", keys[2], keys[2])
	}
}
