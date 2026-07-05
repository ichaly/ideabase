package compiler

import (
	"encoding/base64"
	"testing"
)

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
