package std

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/ichaly/ideabase/std/cache"
	"github.com/ichaly/ideabase/utl"
	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
)

type cacheEntry struct {
	Rows int64           `json:"r"`
	Data json.RawMessage `json:"d"`
	Tags []string        `json:"t"`
}

type cacheTagsKey struct{}

// WithCacheTags 向 context 注入额外的表名 tag
func WithCacheTags(ctx context.Context, tags ...string) context.Context {
	return context.WithValue(ctx, cacheTagsKey{}, tags)
}

func contextTags(ctx context.Context) []string {
	if v, ok := ctx.Value(cacheTagsKey{}).([]string); ok {
		return v
	}
	return nil
}

// GormCache 基于表名 tag 的 GORM 查询缓存插件（桥接 cache.Cache）
type GormCache struct {
	store cache.Cache
	ttl   time.Duration
}

// NewGormCache 创建 GORM 缓存插件，桥接 cache.Cache 接口
func NewGormCache(s cache.Cache) gorm.Plugin {
	return &GormCache{store: s, ttl: 30 * time.Minute}
}

func (my *GormCache) Name() string { return "gorm:cache" }

func (my *GormCache) Initialize(db *gorm.DB) error {
	name := my.Name()
	return errors.Join(
		db.Callback().Query().Replace("gorm:query", my.query),
		db.Callback().Create().After("gorm:create").Register(name+":create", my.purge),
		db.Callback().Update().After("gorm:update").Register(name+":update", my.purge),
		db.Callback().Delete().After("gorm:delete").Register(name+":delete", my.purge),
	)
}

func (my *GormCache) key(db *gorm.DB) string {
	return "sql:" + utl.MD5(db.Dialector.Explain(db.Statement.SQL.String(), db.Statement.Vars...))
}

var tableRe = regexp.MustCompile("(?i)\\b(?:FROM|JOIN|INTO)\\s+(?:[`\"]?(\\w+)|(\\s*\\())")
var cteRe = regexp.MustCompile(`(?i)\bWITH\s+(\w+)\s+AS\s*\(`)

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

func collectTables(sql string, cteNames map[string]struct{}, seen map[string]struct{}) {
	matches := tableRe.FindAllStringSubmatchIndex(sql, -1)
	for _, idx := range matches {
		if idx[2] >= 0 {
			name := strings.ToLower(sql[idx[2]:idx[3]])
			if _, isCTE := cteNames[name]; !isCTE {
				seen[name] = struct{}{}
			}
		} else if idx[4] >= 0 {
			if inner, ok := extractParenContent(sql, idx[4]); ok {
				collectTables(inner, cteNames, seen)
			}
		}
	}
}

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

func (my *GormCache) query(db *gorm.DB) {
	if db.DryRun || db.Error != nil {
		return
	}
	callbacks.BuildQuerySQL(db)
	key := my.key(db)

	if raw, err := my.store.Get(db.Statement.Context, key); err == nil {
		var entry cacheEntry
		if utl.Unmarshal(raw, &entry) == nil {
			if utl.Unmarshal(entry.Data, db.Statement.Dest) == nil {
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
		t := extractTags(db.Statement.SQL.String())
		if extra := contextTags(db.Statement.Context); len(extra) > 0 {
			seen := make(map[string]struct{}, len(t)+len(extra))
			for _, v := range t {
				seen[v] = struct{}{}
			}
			for _, v := range extra {
				if _, ok := seen[v]; !ok {
					t = append(t, v)
				}
			}
		}
		entry := cacheEntry{Rows: db.RowsAffected, Data: data, Tags: t}
		if encoded, err := utl.Marshal(entry); err == nil {
			_ = my.store.Set(db.Statement.Context, key, encoded, my.ttl, t...)
		}
	}
}

func (my *GormCache) purge(db *gorm.DB) {
	if db.Error != nil || db.Statement.Table == "" {
		return
	}
	_ = my.store.Flush(context.Background(), purgeTag(db))
}
