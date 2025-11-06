package std

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/eko/gocache/lib/v4/cache"
	"github.com/eko/gocache/lib/v4/store"
	"github.com/ichaly/ideabase/utl"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
)

var requestGroup singleflight.Group

type keyCacheContext struct{}

type Cache struct {
	Cache       *cache.Cache[string]
	exp         time.Duration
	keyGenerate func(*gorm.DB) string
}
type cachePayload struct {
	RowsAffected int64       `json:"rows_affected"`
	Data         interface{} `json:"data"`
}

// Name `gorm.Plugin` implements.
func (my Cache) Name() string { return "gorm-cache" }

func NewCache(s *cache.Cache[string]) gorm.Plugin {
	return Cache{s, 30 * time.Minute, func(db *gorm.DB) string {
		return fmt.Sprintf(
			"sql:%s",
			utl.MD5(db.Dialector.Explain(db.Statement.SQL.String(), db.Statement.Vars...)),
		)
	}}
}

// Initialize `gorm.Plugin` implements.
func (my Cache) Initialize(db *gorm.DB) error {
	if err := db.Callback().Query().Replace("gorm:query", my.query); err != nil {
		return err
	}
	if err := db.Callback().Create().After("gorm:create").Register(my.Name()+":after_create", my.afterUpdate); err != nil {
		return err
	}
	if err := db.Callback().Update().After("gorm:update").Register(my.Name()+":after_update", my.afterUpdate); err != nil {
		return err
	}
	if err := db.Callback().Delete().After("gorm:delete").Register(my.Name()+":after_delete", my.afterUpdate); err != nil {
		return err
	}
	return nil
}

// query replace gorm:query
func (my Cache) query(db *gorm.DB) {
	if db.DryRun || db.Error != nil {
		return
	}
	callbacks.BuildQuerySQL(db)
	cacheKey := my.keyGenerate(db)

	// get from cache
	if val, err := my.Cache.Get(db.Statement.Context, cacheKey); err == nil {
		if my.loadFromCache(db, []byte(val)) {
			return
		}
	}

	// get from db
	my.queryFromDB(db, cacheKey)

	// add to cache
	encoded, err := utl.Marshal(cachePayload{RowsAffected: db.RowsAffected, Data: db.Statement.Dest})
	if err != nil {
		return
	}
	_ = my.Cache.Set(
		db.Statement.Context, cacheKey, string(encoded),
		store.WithExpiration(my.exp),
		store.WithTags([]string{db.Statement.Table}),
	)
}

func (my Cache) afterUpdate(db *gorm.DB) {
	total := db.Statement.RowsAffected
	if total <= 0 {
		return
	}

	if err := my.Cache.Invalidate(db.Statement.Context, store.WithInvalidateTags([]string{db.Statement.Table})); err != nil {
		_ = db.AddError(err)
	}
}

func (my Cache) queryFromDB(db *gorm.DB, cacheKey string) {
	var (
		rows *sql.Rows
		err  error
	)
	var val interface{}
	val, err, _ = requestGroup.Do(cacheKey, func() (interface{}, error) {
		db.Statement.Context = context.WithValue(db.Statement.Context, keyCacheContext{}, 1)
		return db.Statement.ConnPool.QueryContext(db.Statement.Context, db.Statement.SQL.String(), db.Statement.Vars...)
	})
	rows = val.(*sql.Rows)
	if err != nil {
		_ = db.AddError(err)
		return
	}
	defer func(rows *sql.Rows) {
		_ = rows.Close()
	}(rows)
	gorm.Scan(rows, db, 0)
}

func (my Cache) loadFromCache(db *gorm.DB, raw []byte) bool {
	if len(raw) == 0 {
		return false
	}

	payload := cachePayload{Data: db.Statement.Dest}
	if payload.Data == nil {
		return false
	}

	if err := utl.Unmarshal(raw, &payload); err != nil {
		return false
	}

	db.RowsAffected = payload.RowsAffected
	db.Statement.RowsAffected = payload.RowsAffected
	if db.Statement.Result != nil {
		db.Statement.Result.RowsAffected = payload.RowsAffected
	}

	return true
}
