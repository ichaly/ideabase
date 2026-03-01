package std

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ichaly/ideabase/utl"
	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
)

type cacheEntry struct {
	Rows int64           `json:"r"`
	Data json.RawMessage `json:"d"`
	Tags []string        `json:"t"`
}

// Cache 是基于表名 tag 的 GORM 查询缓存插件。
// 工作原理：
//   - 拦截所有 SELECT，以完整 SQL 的 MD5 为 key 缓存结果，同时把涉及的表名 tag 一并存入缓存
//   - 拦截所有写操作（INSERT/UPDATE/DELETE），按表名 tag 批量失效相关缓存
//
// 这样只要某张表有写入，所有查询过该表的缓存条目都会被自动清除。
type Cache struct {
	store *Storage
	ttl   time.Duration
}

func NewCache(s *Storage) gorm.Plugin {
	return &Cache{store: s, ttl: 30 * time.Minute}
}

func (my *Cache) Name() string { return "gorm:cache" }

func (my *Cache) Initialize(db *gorm.DB) error {
	name := my.Name()
	return errors.Join(
		// 替换默认查询回调，加入缓存读写逻辑
		db.Callback().Query().Replace("gorm:query", my.query),
		// 写操作完成后触发缓存失效（After 确保写入已提交再清缓存）
		db.Callback().Create().After("gorm:create").Register(name+":create", my.purge),
		db.Callback().Update().After("gorm:update").Register(name+":update", my.purge),
		db.Callback().Delete().After("gorm:delete").Register(name+":delete", my.purge),
	)
}

// key 以完整渲染后的 SQL（含参数值）计算 MD5，作为缓存 key。
// 使用 Explain 将占位符替换为实际参数，确保不同参数的查询对应不同 key。
func (my *Cache) key(db *gorm.DB) string {
	return "sql:" + utl.MD5(db.Dialector.Explain(db.Statement.SQL.String(), db.Statement.Vars...))
}

// tableRe 匹配 FROM、JOIN（各种类型）、INTO 后紧跟的表名或子查询起始括号。
// 捕获组1：直接表名（忽略可能的双引号/反引号前缀）；捕获组2：左括号，表示子查询，需递归处理。
var tableRe = regexp.MustCompile("(?i)\\b(?:FROM|JOIN|INTO)\\s+(?:[`\"]?(\\w+)|(\\s*\\())")

// cteRe 匹配 WITH 子句中定义的 CTE 别名，用于从结果中排除这些非真实表名。
// 例：WITH cte AS (...) → 排除 "cte"
var cteRe = regexp.MustCompile(`(?i)\bWITH\s+(\w+)\s+AS\s*\(`)

// extractTags 从完整构建的 SQL 文本中递归提取所有涉及的真实表名。
// 处理以下场景：
//   - 普通查询：FROM tbl / JOIN tbl alias
//   - 子查询：FROM (SELECT ... FROM tbl) sub → 递归进括号提取 tbl
//   - CTE：WITH cte AS (SELECT ... FROM tbl) SELECT ... FROM cte → 排除 cte，提取 tbl
//
// 只在写缓存（SET）时调用一次，结果随缓存条目持久化，HIT 时直接取出复用，无需重复解析。
func extractTags(sql string) []string {
	cteNames := map[string]struct{}{}
	for _, m := range cteRe.FindAllStringSubmatch(sql, -1) {
		cteNames[strings.ToLower(m[1])] = struct{}{}
	}
	seen := map[string]struct{}{}
	collectTables(sql, cteNames, seen)
	result := make([]string, 0, len(seen))
	for t := range seen {
		result = append(result, t)
	}
	return result
}

// collectTables 在给定的 SQL 片段中递归提取所有真实表名，结果写入 seen。
// 遇到子查询（FROM/JOIN 后紧跟左括号）时，提取括号内的内容继续递归。
func collectTables(sql string, cteNames map[string]struct{}, seen map[string]struct{}) {
	matches := tableRe.FindAllStringSubmatchIndex(sql, -1)
	for _, idx := range matches {
		if idx[2] >= 0 {
			// 捕获组1匹配到：直接是表名
			name := strings.ToLower(sql[idx[2]:idx[3]])
			if _, isCTE := cteNames[name]; !isCTE {
				seen[name] = struct{}{}
			}
		} else if idx[4] >= 0 {
			// 捕获组2匹配到左括号：子查询，提取括号内内容递归处理
			start := idx[4] // 左括号位置
			if inner, ok := extractParenContent(sql, start); ok {
				collectTables(inner, cteNames, seen)
			}
		}
	}
}

// extractParenContent 从 sql[start] 处的左括号开始，提取匹配的括号内内容。
// 正确处理嵌套括号，返回括号内的字符串（不含外层括号）。
func extractParenContent(sql string, start int) (string, bool) {
	depth := 0
	for i := start; i < len(sql); i++ {
		switch sql[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return sql[start+1 : i], true
			}
		}
	}
	return "", false
}

// purgeTag 从写操作的 db.Statement 中提取真实表名，用于 PURGE。
// 写操作时 GORM 用模型的 TableName() 填充 Statement.Table（或 Schema.Table），
// 不会出现别名，直接使用；空格截断作为兜底。
func purgeTag(db *gorm.DB) string {
	if db.Statement.Schema != nil && db.Statement.Schema.Table != "" {
		return db.Statement.Schema.Table
	}
	t := db.Statement.Table
	if i := strings.IndexByte(t, ' '); i > 0 {
		return t[:i]
	}
	return t
}

// query 替换 GORM 默认的查询回调，实现缓存读写：
//  1. 先调用 BuildQuerySQL 构建完整 SQL
//  2. 命中缓存则直接还原结果（含 tags），跳过数据库访问
//  3. 未命中则执行真实查询，将结果连同 tags 一起写入缓存
func (my *Cache) query(db *gorm.DB) {
	if db.DryRun || db.Error != nil {
		return
	}
	callbacks.BuildQuerySQL(db)
	key := my.key(db)

	if raw, err := my.store.Get(db.Statement.Context, key); err == nil {
		var entry cacheEntry
		// 第一步：反序列化缓存体，Data 字段保留为原始 JSON 字节（json.RawMessage）
		if utl.Unmarshal(raw, &entry) == nil {
			// 第二步：把 Data 字节反序列化到 db.Statement.Dest（持有正确的目标类型指针）
			// 必须分两步：若 Data 是 interface{}，JSON 无法感知目标类型，
			// 会还原成 map/[]interface{}；用 RawMessage 保留字节再反序列化到具体类型才能正确还原。
			if utl.Unmarshal(entry.Data, db.Statement.Dest) == nil {
				fmt.Printf("[cache] HIT  tags=%v key=%s\n", entry.Tags, key)
				db.RowsAffected = entry.Rows
				db.Statement.RowsAffected = entry.Rows
				return
			}
		}
	}

	rows, err := db.Statement.ConnPool.QueryContext(
		db.Statement.Context, db.Statement.SQL.String(), db.Statement.Vars...,
	)
	if err != nil {
		_ = db.AddError(err)
		return
	}
	defer func() { _ = rows.Close() }()
	gorm.Scan(rows, db, 0)

	if db.Error != nil {
		return
	}
	if data, err := utl.Marshal(db.Statement.Dest); err == nil {
		// tags 在 SQL 完整构建后提取一次，随缓存条目存储，后续 HIT 直接复用
		t := extractTags(db.Statement.SQL.String())
		entry := cacheEntry{Rows: db.RowsAffected, Data: data, Tags: t}
		if encoded, err := utl.Marshal(entry); err == nil {
			fmt.Printf("[cache] SET  tags=%v key=%s\n", t, key)
			_ = my.store.Set(db.Statement.Context, key, encoded, WithExpiration(my.ttl), WithTags(t))
		}
	}
}

// purge 在写操作完成后按表名 tag 批量失效缓存。
// 表名通过 purgeTag 从 Schema.Table 获取，这是 GORM 调用 TableName() 后的真实表名，绝对可靠。
// 使用 context.Background() 而非请求 context，原因：
// 写回调在响应发送后执行，请求 context 可能已被框架取消，
// 若复用会导致 Redis 等后端静默失败，旧缓存一直存活到 TTL 过期。
func (my *Cache) purge(db *gorm.DB) {
	if db.Error != nil || db.Statement.Table == "" {
		return
	}
	t := purgeTag(db)
	fmt.Printf("[cache] PURGE tag=%s\n", t)
	_ = my.store.Invalidate(context.Background(), WithInvalidateTags([]string{t}))
}
