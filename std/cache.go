package std

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/ichaly/ideabase/utl"
	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
)

type cacheEntry struct {
	Rows int64       `json:"r"`
	Data interface{} `json:"d"`
}

// Cache is a GORM plugin that caches SELECT results by SQL fingerprint
// and invalidates by table name on any write (create/update/delete).
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
		db.Callback().Query().Replace("gorm:query", my.query),
		db.Callback().Create().After("gorm:create").Register(name+":create", my.purge),
		db.Callback().Update().After("gorm:update").Register(name+":update", my.purge),
		db.Callback().Delete().After("gorm:delete").Register(name+":delete", my.purge),
	)
}

// key returns an MD5-based cache key from the fully-rendered SQL.
func (my *Cache) key(db *gorm.DB) string {
	return "sql:" + utl.MD5(db.Dialector.Explain(db.Statement.SQL.String(), db.Statement.Vars...))
}

// joinRe extracts the table name immediately after any JOIN keyword.
var joinRe = regexp.MustCompile(`(?i)\bJOIN\s+["\x60]?(\w+)`)

// tag returns the base table name, stripping any alias.
// e.g. "cms_channel_lineage cl" → "cms_channel_lineage"
func tag(db *gorm.DB) string {
	t := db.Statement.Table
	if i := strings.IndexByte(t, ' '); i > 0 {
		return t[:i]
	}
	return t
}

// tags returns the main table tag plus all JOIN table tags extracted from the SQL.
func tags(db *gorm.DB) []string {
	seen := map[string]struct{}{}
	add := func(t string) {
		if t != "" {
			seen[t] = struct{}{}
		}
	}
	add(tag(db))
	for _, m := range joinRe.FindAllStringSubmatch(db.Statement.SQL.String(), -1) {
		add(m[1])
	}
	result := make([]string, 0, len(seen))
	for t := range seen {
		result = append(result, t)
	}
	return result
}

func (my *Cache) query(db *gorm.DB) {
	if db.DryRun || db.Error != nil {
		return
	}
	callbacks.BuildQuerySQL(db)
	key := my.key(db)

	if raw, err := my.store.Get(db.Statement.Context, key); err == nil {
		entry := cacheEntry{Data: db.Statement.Dest}
		if utl.Unmarshal(raw, &entry) == nil {
			db.RowsAffected = entry.Rows
			db.Statement.RowsAffected = entry.Rows
			return
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
	if encoded, err := utl.Marshal(cacheEntry{Rows: db.RowsAffected, Data: db.Statement.Dest}); err == nil {
		_ = my.store.Set(db.Statement.Context, key, encoded, WithExpiration(my.ttl), WithTags(tags(db)))
	}
}

func (my *Cache) purge(db *gorm.DB) {
	if db.Error != nil || db.Statement.Table == "" {
		return
	}
	_ = my.store.Invalidate(db.Statement.Context, WithInvalidateTags([]string{tag(db)}))
}