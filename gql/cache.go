package gql

import (
	"crypto/sha256"
	"sync"

	"github.com/ichaly/ideabase/gql/internal/lru"
	"github.com/vektah/gqlparser/v2/ast"
)

// planKey 缓存键：结构体避免每请求拼接字符串的分配
type planKey struct {
	operation string
	query     [sha256.Size]byte
	shape     string
	view      string
}

func planKeyFor(operation, query, shape, view string) planKey {
	return planKey{operation: operation, query: sha256.Sum256([]byte(query)), shape: shape, view: view}
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
type lruCache[K comparable, V any] = lru.Cache[K, V]

func newLRU[K comparable, V any](limit int) *lruCache[K, V] {
	return lru.New[K, V](limit)
}

type planCache = lruCache[planKey, *planEntry]
type effectiveCache = lruCache[string, *effectiveView]

func newEffectiveCache(limit int) *effectiveCache { return newLRU[string, *effectiveView](limit) }
func newPlanCache(limit int) *planCache           { return newLRU[planKey, *planEntry](limit) }
