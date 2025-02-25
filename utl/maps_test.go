package utl

import (
	"testing"
)

func TestTeeMap_Get(t *testing.T) {
	// 测试不同类型的键
	t.Run("Different types", func(t *testing.T) {
		// 创建一个 string 和 int 类型键的 TeeMap
		teeMap := NewTeeMap[string, int, string]()

		// 设置键值对
		teeMap.Set("key1", 1, "value1")
		teeMap.Set("key2", 2, "value2")

		// 测试通过 string 类型键获取值
		value, ok := teeMap.Get("key1")
		if !ok || value != "value1" {
			t.Errorf("Expected to get 'value1' for key 'key1', got '%v', ok: %v", value, ok)
		}

		// 测试通过 int 类型键获取值
		value, ok = teeMap.Get(1)
		if !ok || value != "value1" {
			t.Errorf("Expected to get 'value1' for key 1, got '%v', ok: %v", value, ok)
		}

		// 测试不存在的键
		_, ok = teeMap.Get("nonexistent")
		if ok {
			t.Error("Expected to not find value for nonexistent key")
		}

		_, ok = teeMap.Get(999)
		if ok {
			t.Error("Expected to not find value for nonexistent key")
		}
	})

	// 测试相同类型的键
	t.Run("Same types", func(t *testing.T) {
		// 创建一个 string 和 string 类型键的 TeeMap
		teeMap := NewTeeMap[string, string, int]()

		// 设置键值对
		teeMap.Set("leftKey", "rightKey", 100)

		// 测试通过左键获取值
		value, ok := teeMap.Get("leftKey")
		if !ok || value != 100 {
			t.Errorf("Expected to get 100 for key 'leftKey', got %v, ok: %v", value, ok)
		}

		// 测试通过右键获取值
		value, ok = teeMap.Get("rightKey")
		if !ok || value != 100 {
			t.Errorf("Expected to get 100 for key 'rightKey', got %v, ok: %v", value, ok)
		}
	})

	// 测试整数类型的键
	t.Run("Integer types", func(t *testing.T) {
		// 创建一个 int 和 int64 类型键的 TeeMap
		teeMap := NewTeeMap[int, int64, string]()

		// 设置键值对
		teeMap.Set(42, int64(42), "answer")

		// 测试通过 int 类型键获取值
		value, ok := teeMap.Get(42)
		if !ok || value != "answer" {
			t.Errorf("Expected to get 'answer' for key 42, got '%v', ok: %v", value, ok)
		}

		// 测试通过 int64 类型键获取值
		value, ok = teeMap.Get(int64(42))
		if !ok || value != "answer" {
			t.Errorf("Expected to get 'answer' for key int64(42), got '%v', ok: %v", value, ok)
		}
	})
}

func TestTeeMap_Delete(t *testing.T) {
	// 创建一个 TeeMap
	teeMap := NewTeeMap[string, int, string]()

	// 设置键值对
	teeMap.Set("key1", 1, "value1")
	teeMap.Set("key2", 2, "value2")

	// 测试通过左键删除
	err := teeMap.Delete("key1")
	if err != nil {
		t.Errorf("Unexpected error when deleting: %v", err)
	}

	// 验证键值对已被删除
	_, ok := teeMap.Get("key1")
	if ok {
		t.Error("Expected key 'key1' to be deleted")
	}

	_, ok = teeMap.Get(1)
	if ok {
		t.Error("Expected key 1 to be deleted")
	}

	// 测试通过右键删除
	err = teeMap.Delete(2)
	if err != nil {
		t.Errorf("Unexpected error when deleting: %v", err)
	}

	// 验证键值对已被删除
	_, ok = teeMap.Get("key2")
	if ok {
		t.Error("Expected key 'key2' to be deleted")
	}

	_, ok = teeMap.Get(2)
	if ok {
		t.Error("Expected key 2 to be deleted")
	}

	// 测试删除不存在的键
	err = teeMap.Delete("nonexistent")
	if err != nil {
		t.Errorf("Expected no error when deleting nonexistent key, got: %v", err)
	}

	// 测试删除不支持的类型
	err = teeMap.Delete(3.14)
	if err == nil {
		t.Error("Expected error when deleting with unsupported type")
	}
}
