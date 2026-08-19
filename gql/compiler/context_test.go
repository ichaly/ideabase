package compiler

import (
	"encoding/base64"
	"errors"
	"fmt"
	"testing"

	"github.com/ichaly/ideabase/gql/protocol"
)

type contextHoster struct{}

func (contextHoster) PutNode(string, *protocol.Class) error  { return nil }
func (contextHoster) GetNode(string) (*protocol.Class, bool) { return nil, false }
func (contextHoster) SetVersion(string)                      {}
func (contextHoster) SearchMode() (string, string)           { return "simple", "english" }
func (contextHoster) DefaultLimit() int                      { return 25 }
func (contextHoster) SchemaName() string                     { return "public" }
func (contextHoster) LookupOperation(string) (OperationSpec, bool) {
	return OperationSpec{Operation: "read"}, true
}

func TestContextStateAndSlotForms(t *testing.T) {
	ctx := NewContext(contextHoster{}, "\"", map[string]interface{}{"name": "Ada", "ids": []any{int64(1), int64(2)}})
	defer ctx.Release()
	mode, config := ctx.SearchMode()
	if mode != "simple" || config != "english" {
		t.Fatal("search mode not delegated")
	}
	if ctx.DefaultLimit() != 25 || ctx.SchemaName() != "public" {
		t.Fatal("host capabilities not delegated")
	}
	if _, ok := ctx.GetClass("missing"); ok {
		t.Fatal("unexpected class")
	}
	if spec, ok := ctx.LookupOperation("Query.users"); !ok || spec.Operation != "read" {
		t.Fatal("operation lookup failed")
	}
	ctx.MarkTable("users")
	ctx.MarkTable("users")
	ctx.MarkTable("posts")
	if got := fmt.Sprint(ctx.Tables()); got != "[users posts]" {
		t.Fatalf("tables=%s", got)
	}
	ctx.MarkVolatile()
	if !ctx.Volatile() {
		t.Fatal("volatile flag not set")
	}
	if ctx.NextIndex() != 0 || ctx.NextIndex() != 1 {
		t.Fatal("index sequence incorrect")
	}
	if ctx.AddParam(1) != 1 || ctx.AddVariable("name") != 2 || ctx.AddListVariable("ids") != 3 {
		t.Fatal("slot indexes incorrect")
	}
	if ctx.AddContextSlot("tenant") != 4 || ctx.AddContextSlot("tenant") != 4 {
		t.Fatal("context slot was not deduplicated")
	}
	if ctx.AddRequiredContextSlot("actor") != 5 || ctx.AddContextListSlot("tenant") != 4 {
		t.Fatal("context slot variants incorrect")
	}
	if ctx.AddGenerated(func() (any, error) { return "id", nil }) != 6 {
		t.Fatal("generated slot index incorrect")
	}
	ctx.Write("x", 1, int64(2), 3.5, true, false, []byte("z"), fmt.Errorf("e"))
	ctx.Wrap("(", "ok").Space("next").Quote("a\"b").Column("t", "c")
	if ctx.String() == "" {
		t.Fatal("context did not write SQL")
	}
	ctx.SetCursorFingerprint("fp")
	ctx.SetCursorRoot("Query.users")
	ctx.MarkCursor()
	ctx.MarkAudit()
	if ctx.CursorFingerprint() != "fp" || !ctx.UsesCursor() || !ctx.UsesAudit() || ctx.CursorCoordinate("id") != "Query.users.id" {
		t.Fatal("cursor metadata not retained")
	}
	ctx.SetCursorContract(CursorContract{Tenant: "t", Sort: []string{"id:ASC"}})
	if contract, ok := ctx.CursorContract(); !ok || contract.Tenant != "t" {
		t.Fatal("cursor contract not retained")
	}
	if value, ok := ctx.Variable("name"); !ok || value != "Ada" {
		t.Fatal("variable lookup failed")
	}
	if !ShapeBulk.Bulk() || !ShapeBulkResult.Bulk() || !ShapeBulkResult.Result() || !ShapeSingleByPK.Single() || ShapeSingle.Bulk() || ShapeSingle.Result() {
		t.Fatal("operation shape helpers are inconsistent")
	}
	if len(ctx.Slots()) != 6 {
		t.Fatalf("slot copy length = %d", len(ctx.Slots()))
	}
	ctx.AddCursorValue("cursor", 0)
	ctx.QuotedWithSpace("value")
}

func TestResolveSlotsGeneratesContextAndListValues(t *testing.T) {
	slots := []Slot{
		{Generate: func() (any, error) { return int64(9), nil }},
		{Generate: func() (any, error) { return nil, errors.New("boom") }},
	}
	if _, err := ResolveSlots(slots, nil, nil); err == nil {
		t.Fatal("generator errors must propagate")
	}
	args, err := ResolveSlots([]Slot{{Context: "tenant", Required: true}, {Context: "ids", List: true}, {Variable: "ids", List: true, Cursor: -1}, {Value: "x", Cursor: -1}}, map[string]interface{}{"ids": []any{int64(1), int64(2)}}, map[string]interface{}{"tenant": "t"})
	if err != nil || args[0] != "t" {
		t.Fatalf("context slots = %#v, err=%v", args, err)
	}
	if _, err := ResolveSlots([]Slot{{Context: "tenant", Required: true}}, nil, map[string]interface{}{}); err == nil {
		t.Fatal("missing required context must fail")
	}
	if _, err := ResolveSlots([]Slot{{Variable: "cursor", Cursor: 0}}, map[string]interface{}{"cursor": 4}, nil); err == nil {
		t.Fatal("non-string cursor must fail")
	}
}

func TestListArgForms(t *testing.T) {
	for _, value := range []any{nil, "x", []any{int64(1), int64(2)}, []any{1, "x"}, []string{"x"}, []int64{1}, []float64{1}} {
		if ListArg(value) == nil {
			t.Fatalf("ListArg(%#v) returned nil", value)
		}
	}
}

func TestContextReleaseClearsPooledSlotReferences(t *testing.T) {
	ctx := NewContext(nil, `"`, nil)
	ctx.slots = append(ctx.slots, Slot{Value: &struct{}{}, Generate: protocol.IDGenerator(func() (any, error) { return 1, nil }), CursorContract: &CursorContract{Tenant: "t"}})
	retained := ctx.slots[:cap(ctx.slots)]
	ctx.Release()
	if retained[0].Value != nil || retained[0].Generate != nil || retained[0].CursorContract != nil {
		t.Fatal("released context retained slot references")
	}
}

func TestQuoteEscapesEmbeddedDoubleQuotes(t *testing.T) {
	ctx := NewContext(nil, `"`, nil)
	defer ctx.Release()
	ctx.Quote(`a"b`)
	if got := ctx.String(); got != `"a""b"` {
		t.Fatalf("quoted identifier = %q, want %q", got, `"a""b"`)
	}
}

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
