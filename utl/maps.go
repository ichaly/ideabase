package utl

import "strings"

type AnyMap[V any] map[string]V

// TeeMap 用于存储两个不同类型的key对应同一个value的映射
type TeeMap[L comparable, R comparable, V any] struct {
	leftMap    map[L]V
	rightMap   map[R]V
	reverseMap map[interface{}]interface{}
}

// NewTeeMap 创建一个新的BiKeyMap实例
func NewTeeMap[L comparable, R comparable, V any]() *TeeMap[L, R, V] {
	return &TeeMap[L, R, V]{
		leftMap:    make(map[L]V),
		rightMap:   make(map[R]V),
		reverseMap: make(map[interface{}]interface{}),
	}
}

// Set 用于设置键值对，两个不同类型的key对应同一个value
func (my *TeeMap[L, R, V]) Set(leftKey L, rightKey R, value V) {
	my.leftMap[leftKey] = value
	my.rightMap[rightKey] = value
	my.reverseMap[leftKey] = rightKey
	my.reverseMap[rightKey] = leftKey
}

// Get 用于通过任一key获取对应的value
func (my *TeeMap[L, R, V]) Get(key interface{}) (V, bool) {
	var value V
	var ok bool

	switch k := key.(type) {
	case L:
		value, ok = my.leftMap[k]
	case R:
		value, ok = my.rightMap[k]
	}

	return value, ok
}

// Delete 用于通过任一key删除对应的键值对
func (my *TeeMap[L, R, V]) Delete(key interface{}) {
	if otherKey, ok := my.reverseMap[key]; ok {
		switch k := key.(type) {
		case L:
			delete(my.leftMap, k)
			delete(my.rightMap, otherKey.(R))
		case R:
			delete(my.rightMap, k)
			delete(my.leftMap, otherKey.(L))
		}
		delete(my.reverseMap, key)
		delete(my.reverseMap, otherKey)
	}
}

func QueryMap(data map[string]interface{}, path string) interface{} {
	arr := strings.SplitN(path, ".", 2)
	if len(arr) <= 1 {
		return data[path]
	}
	if val, ok := data[arr[0]].(map[string]interface{}); ok {
		return QueryMap(val, arr[1])
	}
	return nil
}

func EraseMap(data map[string]interface{}, path string) interface{} {
	arr := strings.SplitN(path, ".", 2)
	if len(arr) <= 1 {
		res := data[path]
		delete(data, path)
		return res
	}
	if val, ok := data[arr[0]].(map[string]interface{}); ok {
		return EraseMap(val, arr[1])
	}
	return nil
}
