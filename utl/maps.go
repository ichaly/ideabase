package utl

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"golang.org/x/exp/constraints"
)

func MapKeys[M ~map[K]V, K comparable, V any](m M) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func SortKeys[M ~map[K]V, K constraints.Ordered, V any](m M) []K {
	keys := MapKeys(m)
	sort.Slice(keys, func(i, j int) bool {
		return keys[i] < keys[j]
	})
	return keys
}

// AnyMap 是一个字符串键的泛型map
type AnyMap[V any] map[string]V

// TeeMap 用于存储两个不同类型的key对应同一个value的映射
type TeeMap[L comparable, R comparable, V any] struct {
	mu         sync.RWMutex
	leftMap    map[L]V
	rightMap   map[R]V
	reverseMap struct {
		left  map[L]R
		right map[R]L
	}
}

// NewTeeMap 创建一个新的TeeMap实例
func NewTeeMap[L comparable, R comparable, V any]() *TeeMap[L, R, V] {
	return &TeeMap[L, R, V]{
		leftMap:  make(map[L]V),
		rightMap: make(map[R]V),
		reverseMap: struct {
			left  map[L]R
			right map[R]L
		}{
			left:  make(map[L]R),
			right: make(map[R]L),
		},
	}
}

// Set 用于设置键值对，两个不同类型的key对应同一个value
func (my *TeeMap[L, R, V]) Set(leftKey L, rightKey R, value V) {
	my.mu.Lock()
	defer my.mu.Unlock()

	my.leftMap[leftKey] = value
	my.rightMap[rightKey] = value
	my.reverseMap.left[leftKey] = rightKey
	my.reverseMap.right[rightKey] = leftKey
}

// Get 通过任意类型的键获取值
func (my *TeeMap[L, R, V]) Get(key interface{}) (V, bool) {
	my.mu.RLock()
	defer my.mu.RUnlock()

	var value V
	var ok bool

	// 先尝试作为左键查找
	if leftKey, isLeft := key.(L); isLeft {
		if value, ok = my.leftMap[leftKey]; ok {
			return value, ok
		}
	}

	// 如果左键未找到或不是左键类型，尝试作为右键查找
	if rightKey, isRight := key.(R); isRight {
		value, ok = my.rightMap[rightKey]
	}

	return value, ok
}

// Delete 用于通过任一key删除对应的键值对
func (my *TeeMap[L, R, V]) Delete(key interface{}) error {
	my.mu.Lock()
	defer my.mu.Unlock()

	switch k := key.(type) {
	case L:
		if rightKey, ok := my.reverseMap.left[k]; ok {
			delete(my.leftMap, k)
			delete(my.rightMap, rightKey)
			delete(my.reverseMap.left, k)
			delete(my.reverseMap.right, rightKey)
		}
	case R:
		if leftKey, ok := my.reverseMap.right[k]; ok {
			delete(my.rightMap, k)
			delete(my.leftMap, leftKey)
			delete(my.reverseMap.right, k)
			delete(my.reverseMap.left, leftKey)
		}
	default:
		return fmt.Errorf("unsupported key type: %T", key)
	}
	return nil
}

// QueryMap 递归查询嵌套map中的值
func QueryMap(data map[string]interface{}, path string) (interface{}, error) {
	if data == nil {
		return nil, fmt.Errorf("nil map")
	}

	if path == "" {
		return nil, fmt.Errorf("empty path")
	}

	parts := strings.SplitN(path, ".", 2)
	key := parts[0]

	value, ok := data[key]
	if !ok {
		return nil, fmt.Errorf("key not found: %s", key)
	}

	if len(parts) == 1 {
		return value, nil
	}

	nextData, ok := value.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("value at '%s' is not a map", key)
	}

	return QueryMap(nextData, parts[1])
}

// EraseMap 递归删除嵌套map中的值并返回被删除的值
func EraseMap(data map[string]interface{}, path string) (interface{}, error) {
	if data == nil {
		return nil, fmt.Errorf("nil map")
	}

	if path == "" {
		return nil, fmt.Errorf("empty path")
	}

	parts := strings.SplitN(path, ".", 2)
	key := parts[0]

	if len(parts) == 1 {
		value, exists := data[key]
		if !exists {
			return nil, fmt.Errorf("key not found: %s", key)
		}
		delete(data, key)
		return value, nil
	}

	value, ok := data[key]
	if !ok {
		return nil, fmt.Errorf("key not found: %s", key)
	}

	nextData, ok := value.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("value at '%s' is not a map", key)
	}

	return EraseMap(nextData, parts[1])
}
