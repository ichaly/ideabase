package gql

import (
	"container/list"
	"sync"
)

// planCache 执行计划LRU缓存：命中路径零解析、零编译
type planCache struct {
	mu    sync.Mutex
	limit int
	items map[string]*list.Element
	order *list.List // 最近使用在前
}

// cacheEntry 缓存项
type cacheEntry struct {
	key  string
	plan *Plan
}

// newPlanCache 创建指定容量的计划缓存
func newPlanCache(limit int) *planCache {
	return &planCache{
		limit: limit,
		items: make(map[string]*list.Element, limit),
		order: list.New(),
	}
}

// Get 查找计划并刷新热度
func (my *planCache) Get(key string) (*Plan, bool) {
	my.mu.Lock()
	defer my.mu.Unlock()

	element, ok := my.items[key]
	if !ok {
		return nil, false
	}
	my.order.MoveToFront(element)
	return element.Value.(*cacheEntry).plan, true
}

// Put 写入计划，超出容量时淘汰最久未用项
func (my *planCache) Put(key string, plan *Plan) {
	my.mu.Lock()
	defer my.mu.Unlock()

	if element, ok := my.items[key]; ok {
		element.Value.(*cacheEntry).plan = plan
		my.order.MoveToFront(element)
		return
	}

	my.items[key] = my.order.PushFront(&cacheEntry{key: key, plan: plan})
	if my.order.Len() > my.limit {
		oldest := my.order.Back()
		my.order.Remove(oldest)
		delete(my.items, oldest.Value.(*cacheEntry).key)
	}
}
