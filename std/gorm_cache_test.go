package std

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/ichaly/ideabase/std/cache"
	_ "github.com/ichaly/ideabase/std/cache/memory"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type cacheUser struct {
	ID   int64  `gorm:"primaryKey"`
	Name string `gorm:"size:30"`
}

// blockConn 在 fail=true 时拒绝查询，确保第二次查询只能依赖缓存。
type blockConn struct {
	*sql.DB
	fail bool
	err  error
}

func (my *blockConn) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	if my.fail {
		return nil, my.err
	}
	return my.DB.QueryContext(ctx, query, args...)
}

func (my *blockConn) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return my.DB.PrepareContext(ctx, query)
}

func (my *blockConn) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	if my.fail {
		return nil, my.err
	}
	return my.DB.ExecContext(ctx, query, args...)
}

func (my *blockConn) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	if my.fail {
		// 返回一个包含错误的 Row，调用方在 Scan 时会看到错误
		return (&sql.DB{}).QueryRowContext(ctx, query, args...)
	}
	return my.DB.QueryRowContext(ctx, query, args...)
}

func openTestDB(t testing.TB) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=private"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	return db
}

func TestCacheSecondLoadShouldReturnData(t *testing.T) {
	db := openTestDB(t)

	store, err := cache.New(nil)
	require.NoError(t, err)

	err = db.Use(NewGormCache(store))
	require.NoError(t, err)

	require.NoError(t, db.AutoMigrate(&cacheUser{}))
	require.NoError(t, db.Create(&cacheUser{ID: 1, Name: "Tom"}).Error)

	sqlDb, err := db.DB()
	require.NoError(t, err)

	conn := &blockConn{DB: sqlDb, err: errors.New("should hit cache instead of database")}
	db.ConnPool = conn
	db.Statement.ConnPool = db.ConnPool

	var first []cacheUser
	err = db.Find(&first).Error
	require.NoError(t, err)
	require.Len(t, first, 1, "第一次从数据库读取应该返回数据并写入缓存")

	// 关闭数据库访问，强制依赖缓存
	conn.fail = true

	var cached []cacheUser
	err = db.Find(&cached).Error
	require.NoError(t, err, "缓存读取失败时会继续访问数据库导致报错")
	require.Len(t, cached, 1, "第二次命中缓存仍然返回空数据")
}

func TestCacheInvalidatesOnWrite(t *testing.T) {
	db := openTestDB(t)

	store, err := cache.New(nil)
	require.NoError(t, err)

	err = db.Use(NewGormCache(store))
	require.NoError(t, err)

	require.NoError(t, db.AutoMigrate(&cacheUser{}))
	require.NoError(t, db.Create(&cacheUser{ID: 1, Name: "Tom"}).Error)

	// 第一次查询写入缓存
	var first []cacheUser
	require.NoError(t, db.Find(&first).Error)
	require.Len(t, first, 1)

	// 写入新记录，触发 purge 清空缓存
	require.NoError(t, db.Create(&cacheUser{ID: 2, Name: "Jerry"}).Error)

	// 缓存已失效，再次查询应命中数据库，拿到最新 2 条
	var updated []cacheUser
	require.NoError(t, db.Find(&updated).Error)
	require.Len(t, updated, 2, "写入后缓存应失效，查询应返回最新数据")
}

func TestCacheInvalidatesOnDelete(t *testing.T) {
	db := openTestDB(t)

	store, err := cache.New(nil)
	require.NoError(t, err)

	err = db.Use(NewGormCache(store))
	require.NoError(t, err)

	require.NoError(t, db.AutoMigrate(&cacheUser{}))
	require.NoError(t, db.Create(&cacheUser{ID: 1, Name: "Tom"}).Error)
	require.NoError(t, db.Create(&cacheUser{ID: 2, Name: "Jerry"}).Error)

	// 第一次查询写入缓存
	var first []cacheUser
	require.NoError(t, db.Find(&first).Error)
	require.Len(t, first, 2)

	// 删除一条记录，触发 purge 清空缓存
	require.NoError(t, db.Delete(&cacheUser{}, 2).Error)

	// 缓存已失效，再次查询应拿到最新 1 条
	var updated []cacheUser
	require.NoError(t, db.Find(&updated).Error)
	require.Len(t, updated, 1, "删除后缓存应失效，查询应返回最新数据")
}

func BenchmarkCacheHit(b *testing.B) {
	db := openTestDB(b)

	store, _ := cache.New(nil)
	_ = db.Use(NewGormCache(store))
	_ = db.AutoMigrate(&cacheUser{})
	for i := 1; i <= 100; i++ {
		_ = db.Create(&cacheUser{ID: int64(i), Name: "user"}).Error
	}

	// 预热缓存
	var warmup []cacheUser
	_ = db.Find(&warmup).Error

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var list []cacheUser
		_ = db.Find(&list).Error
	}
}

func BenchmarkCacheMiss(b *testing.B) {
	db := openTestDB(b)

	store, _ := cache.New(nil)
	_ = db.Use(NewGormCache(store))
	_ = db.AutoMigrate(&cacheUser{})
	for i := 1; i <= 100; i++ {
		_ = db.Create(&cacheUser{ID: int64(i), Name: "user"}).Error
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// 每次用不同 offset 绕过缓存，模拟 miss
		var list []cacheUser
		_ = db.Offset(i % 10).Find(&list).Error
	}
}

func TestExtractTags(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{
			name: "普通查询",
			sql:  `SELECT * FROM users WHERE id = 1`,
			want: []string{"users"},
		},
		{
			name: "带别名",
			sql:  `SELECT * FROM cms_channel_lineage cl JOIN cms_lineage l ON l.id = cl.lineage_id`,
			want: []string{"cms_channel_lineage", "cms_lineage"},
		},
		{
			name: "子查询",
			sql:  `SELECT * FROM (SELECT id FROM cms_lineage WHERE state = 1) sub`,
			want: []string{"cms_lineage"},
		},
		{
			name: "CTE",
			sql:  `WITH cte AS (SELECT id FROM cms_lineage) SELECT * FROM cte`,
			want: []string{"cms_lineage"},
		},
		{
			name: "CTE 加 JOIN",
			sql:  `WITH cte AS (SELECT id FROM cms_lineage) SELECT * FROM cte JOIN cms_channel c ON c.id = 1`,
			want: []string{"cms_lineage", "cms_channel"},
		},
		{
			name: "嵌套子查询",
			sql:  `SELECT * FROM (SELECT * FROM (SELECT id FROM orders) t1) t2`,
			want: []string{"orders"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractTags(c.sql)
			require.ElementsMatch(t, c.want, got)
		})
	}
}
