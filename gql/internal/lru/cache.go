package lru

import (
	"container/list"
	"sync"
)

type entry[K comparable, V any] struct {
	key   K
	value V
}

// Cache is a bounded, concurrency-safe least-recently-used cache.
type Cache[K comparable, V any] struct {
	mu    sync.Mutex
	limit int
	items map[K]*list.Element
	order *list.List
}

func New[K comparable, V any](limit int) *Cache[K, V] {
	if limit < 1 {
		limit = 1
	}
	return &Cache[K, V]{limit: limit, items: make(map[K]*list.Element, limit), order: list.New()}
}

func (cache *Cache[K, V]) Get(key K) (V, bool) {
	return cache.GetIf(key, func(V) bool { return true })
}

// GetIf returns and promotes a value only when valid accepts it; rejected
// entries are removed atomically with the lookup.
func (cache *Cache[K, V]) GetIf(key K, valid func(V) bool) (V, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	element, ok := cache.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	value := element.Value.(*entry[K, V]).value
	if !valid(value) {
		delete(cache.items, key)
		cache.order.Remove(element)
		var zero V
		return zero, false
	}
	cache.order.MoveToFront(element)
	return value, true
}

func (cache *Cache[K, V]) Put(key K, value V) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if element, ok := cache.items[key]; ok {
		element.Value.(*entry[K, V]).value = value
		cache.order.MoveToFront(element)
		return
	}
	cache.items[key] = cache.order.PushFront(&entry[K, V]{key: key, value: value})
	if cache.order.Len() > cache.limit {
		oldest := cache.order.Back()
		cache.order.Remove(oldest)
		delete(cache.items, oldest.Value.(*entry[K, V]).key)
	}
}

func (cache *Cache[K, V]) Len() int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.order.Len()
}

func (cache *Cache[K, V]) Delete(key K) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if element, ok := cache.items[key]; ok {
		delete(cache.items, key)
		cache.order.Remove(element)
	}
}
