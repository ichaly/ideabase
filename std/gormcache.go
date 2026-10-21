package std

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ichaly/ideabase/std/cache"
	"github.com/ichaly/ideabase/std/internal/sql"
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
	tags, _ := ctx.Value(cacheTagsKey{}).([]string)
	return tags
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
		db.Callback().Raw().After("gorm:raw").Register(name+":raw", my.purgeRaw),
		db.Callback().Create().After("gorm:create").Register(name+":create", my.purge),
		db.Callback().Update().After("gorm:update").Register(name+":update", my.purge),
		db.Callback().Delete().After("gorm:delete").Register(name+":delete", my.purge),
	)
}

func (my *GormCache) key(db *gorm.DB) string {
	return "sql:v2:" + db.Dialector.Name() + ":" + utl.MD5(db.Dialector.Explain(db.Statement.SQL.String(), db.Statement.Vars...))
}

func purgeTags(db *gorm.DB) []string {
	stmt := db.Statement
	if stmt.Table == "" {
		return nil
	}
	from := stmt.Quote(stmt.Table)
	if stmt.TableExpr != nil {
		// Table("users AS u") 的 Statement.Table 是别名，实际表来自 TableExpr。
		from = db.Dialector.Explain(stmt.TableExpr.SQL, stmt.TableExpr.Vars...)
	}
	tags, _ := sql.Tags(db.Dialector.Name(), "SELECT * FROM "+from)
	return tags
}

func (my *GormCache) query(db *gorm.DB) {
	if db.Error != nil {
		return
	}
	callbacks.BuildQuerySQL(db)
	// GORM 通过 DryRun 构建子查询，必须先生成 SQL，再跳过缓存和数据库访问。
	if db.DryRun || db.Error != nil {
		return
	}
	key := my.key(db)
	tags, cacheable := sql.Tags(db.Dialector.Name(), db.Statement.SQL.String())
	cacheable = cacheable && my.store != nil
	if cacheable {
		if raw, err := my.store.Get(db.Statement.Context, key); err == nil {
			var entry cacheEntry
			if utl.Unmarshal(raw, &entry) == nil && utl.Unmarshal(entry.Data, db.Statement.Dest) == nil {
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

	if db.Error != nil || !cacheable {
		return
	}
	data, err := utl.Marshal(db.Statement.Dest)
	if err != nil {
		return
	}
	if extra := contextTags(db.Statement.Context); len(extra) > 0 {
		seen := make(map[string]struct{}, len(tags)+len(extra))
		for _, tag := range tags {
			seen[tag] = struct{}{}
		}
		for _, tag := range extra {
			if _, ok := seen[tag]; !ok {
				tags = append(tags, tag)
				seen[tag] = struct{}{}
			}
		}
	}
	entry := cacheEntry{Rows: db.RowsAffected, Data: data, Tags: tags}
	if encoded, err := utl.Marshal(entry); err == nil {
		_ = my.store.Set(db.Statement.Context, key, encoded, my.ttl, tags...)
	}
}

func (my *GormCache) purge(db *gorm.DB) {
	if db.Error != nil || db.DryRun || my.store == nil {
		return
	}
	if tags := purgeTags(db); len(tags) > 0 {
		_ = my.store.Flush(context.Background(), tags...)
	}
}

func (my *GormCache) purgeRaw(db *gorm.DB) {
	if db.Error != nil || db.DryRun || my.store == nil {
		return
	}
	// Exec 没有可靠的 Statement.Table，必须从已执行的 SQL 中提取关系。
	if tags, ok := sql.WriteTags(db.Dialector.Name(), db.Statement.SQL.String()); ok && len(tags) > 0 {
		_ = my.store.Flush(context.Background(), tags...)
	}
}
