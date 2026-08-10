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
	shape     string
	view      string
}

// planEntry 缓存值：恒存operation（变量声明的唯一事实源，入参解码用）；
// 非volatile另存编译成品，volatile执行期仅重做SQL构建（binding与codec路径树复用）
type planEntry struct {
	plan         *Plan                    // 编译成品，volatile时为nil
	operation    *ast.OperationDefinition // 已解析展开的AST
	intro        *ast.OperationDefinition // 与业务字段混排的自省投影
	resolvers    []binding                // resolver绑定只依赖AST，volatile重编译复用（非volatile时plan内已含，恒存仅为构造统一）
	decode       decodeTrie               // resolver宿主路径树只构建一次
	paths        codecPaths               // codec路径树只依赖AST，volatile重编译复用（同上）
	rootResolver bool                     // 自定义Query/Mutation根字段Resolver
	rootPlans    sync.Map                 // 多根执行中按字段缓存非volatile子计划（key: *ast.Field）
}

// effectiveCache bounds identity-shaped schema artifacts. Schema construction
// is relatively expensive and an unbounded map lets high-cardinality tenants
// retain every shape forever.
type lruCache[K comparable, V any] struct {
	mu    sync.Mutex
	limit int
	items map[K]*list.Element
	order *list.List
}

type lruCacheEntry[K comparable, V any] struct {
	key   K
	value V
}

func newLRU[K comparable, V any](limit int) *lruCache[K, V] {
	if limit < 1 {
		limit = 1
	}
	return &lruCache[K, V]{limit: limit, items: make(map[K]*list.Element, limit), order: list.New()}
}

func (my *lruCache[K, V]) Get(key K) (V, bool) {
	my.mu.Lock()
	defer my.mu.Unlock()
	e, ok := my.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	my.order.MoveToFront(e)
	return e.Value.(*lruCacheEntry[K, V]).value, true
}

func (my *lruCache[K, V]) Put(key K, value V) {
	my.mu.Lock()
	defer my.mu.Unlock()
	if e, ok := my.items[key]; ok {
		e.Value.(*lruCacheEntry[K, V]).value = value
		my.order.MoveToFront(e)
		return
	}
	my.items[key] = my.order.PushFront(&lruCacheEntry[K, V]{key: key, value: value})
	if my.order.Len() > my.limit {
		e := my.order.Back()
		my.order.Remove(e)
		delete(my.items, e.Value.(*lruCacheEntry[K, V]).key)
	}
}

type planCache = lruCache[planKey, *planEntry]
type effectiveCache = lruCache[string, *effectiveView]

func newEffectiveCache(limit int) *effectiveCache { return newLRU[string, *effectiveView](limit) }
func newPlanCache(limit int) *planCache           { return newLRU[planKey, *planEntry](limit) }
