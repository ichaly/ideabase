package gql

import (
	stdjson "encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type exampleCapability struct {
	ID        string   `json:"id"`
	Artifacts []string `json:"artifacts"`
}

func TestExamplesCoverDocumentedCapabilities(t *testing.T) {
	want := []string{
		"crud", "batch-upsert", "nested-write", "filters", "sorting", "offset-pagination",
		"cursor-pagination", "aggregates", "search", "relations", "recursive", "distinct-json",
		"variables-fragments", "field-resolver", "batch-resolver", "root-resolver", "resolver-wrap",
		"codec", "id-generation", "remote", "scope", "persisted", "subscription", "introspection", "safety", "bytes",
	}

	raw, err := os.ReadFile(filepath.Join("examples", "coverage.json"))
	if err != nil {
		t.Fatalf("完整示例缺少能力清单: %v", err)
	}
	var capabilities []exampleCapability
	if err = stdjson.Unmarshal(raw, &capabilities); err != nil {
		t.Fatalf("能力清单格式错误: %v", err)
	}

	covered := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		if covered[capability.ID] {
			t.Errorf("能力重复声明: %s", capability.ID)
		}
		covered[capability.ID] = true
		if len(capability.Artifacts) == 0 {
			t.Errorf("能力没有可验证样例: %s", capability.ID)
		}
		for _, artifact := range capability.Artifacts {
			if _, err := os.Stat(filepath.Join("examples", artifact)); err != nil {
				t.Errorf("能力 %s 的样例不存在: %s", capability.ID, artifact)
			}
		}
	}
	for _, id := range want {
		if !covered[id] {
			t.Errorf("README声明的能力未被示例覆盖: %s", id)
		}
	}
}
