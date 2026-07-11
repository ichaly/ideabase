package gql

import (
	"container/list"
	"sync"

	"github.com/vektah/gqlparser/v2/ast"
)

// planKey 缓存键：结构体避免每请求拼接字符串的分配
type planKey struct {
	operation string
	query     string
}

// planEntry 缓存值：恒存operation（变量声明的唯一事实源，入参解码用）；
// 非volatile另存编译成品，volatile执行期仅重做SQL构建（binding与codec路径树复用）
type planEntry struct {
	plan         *Plan                    // 编译成品，volatile时为nil
	operation    *ast.OperationDefinition // 已解析展开的AST
	resolvers    []binding                // resolver绑定只依赖AST，volatile重编译复用（非volatile时plan内已含，恒存仅为构造统一）
	paths        codecPaths               // codec路径树只依赖AST，volatile重编译复用（同上）
	rootResolver bool                     // 自定义Query/Mutation根字段Resolver
}

// planCache 执行计划LRU缓存：命中路径零解析；非volatile零编译
type planCache struct {
	mu    sync.Mutex
	limit int
	items map[planKey]*list.Element
	order *list.List // 最近使用在前
}

// cacheEntry 缓存项
type cacheEntry struct {
	key   planKey
	value *planEntry
}

// newPlanCache 创建指定容量的计划缓存
func newPlanCache(limit int) *planCache {
	return &planCache{
		limit: limit,
		items: make(map[planKey]*list.Element, limit),
		order: list.New(),
	}
}

// Get 查找缓存项并刷新热度
func (my *planCache) Get(key planKey) (*planEntry, bool) {
	my.mu.Lock()
	defer my.mu.Unlock()

	element, ok := my.items[key]
	if !ok {
		return nil, false
	}
	my.order.MoveToFront(element)
	return element.Value.(*cacheEntry).value, true
}

// Put 写入缓存项，超出容量时淘汰最久未用项
func (my *planCache) Put(key planKey, value *planEntry) {
	my.mu.Lock()
	defer my.mu.Unlock()

	if element, ok := my.items[key]; ok {
		element.Value.(*cacheEntry).value = value
		my.order.MoveToFront(element)
		return
	}

	my.items[key] = my.order.PushFront(&cacheEntry{key: key, value: value})
	if my.order.Len() > my.limit {
		oldest := my.order.Back()
		my.order.Remove(oldest)
		delete(my.items, oldest.Value.(*cacheEntry).key)
	}
}
