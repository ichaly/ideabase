package std

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

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

// dialectAlias keeps the SQLite test backend while exercising the MySQL parser.
type dialectAlias struct{ gorm.Dialector }

func (d dialectAlias) Name() string { return "mysql" }

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
	db.Dialector = dialectAlias{db.Dialector}
	return db
}

func TestCacheDryRunBuildsSQLWithoutIO(t *testing.T) {
	db := openTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	// nil 缓存和已关闭的数据库确保 DryRun 只生成 SQL。
	require.NoError(t, db.Use(NewGormCache(nil)))

	var users []cacheUser
	result := db.Session(&gorm.Session{DryRun: true}).Where("name = ?", "Tom").Find(&users)
	require.NoError(t, result.Error)
	require.Contains(t, result.Statement.SQL.String(), "SELECT")
	require.Contains(t, result.Statement.SQL.String(), "WHERE name = ?")
	require.Equal(t, []interface{}{"Tom"}, result.Statement.Vars)
}

func TestCacheExecBuildsSubquery(t *testing.T) {
	db := openTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&cacheUser{}))
	require.NoError(t, db.Create(&[]cacheUser{{ID: 1, Name: "Tom"}, {ID: 2, Name: "Jerry"}}).Error)
	require.NoError(t, db.Use(NewGormCache(nil)))

	subquery := db.Model(&cacheUser{}).Scopes(func(tx *gorm.DB) *gorm.DB {
		return tx.Where("name = ?", "Tom")
	}).Select("id").Group("id")
	result := db.Exec("UPDATE cache_users SET name = ? FROM (?) AS selected WHERE cache_users.id = selected.id", "Updated", subquery)
	require.NoError(t, result.Error)
	require.EqualValues(t, 1, result.RowsAffected)

	var users []cacheUser
	require.NoError(t, db.Raw("SELECT id, name FROM cache_users ORDER BY id").Scan(&users).Error)
	require.Equal(t, []cacheUser{{ID: 1, Name: "Updated"}, {ID: 2, Name: "Jerry"}}, users)
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
	firstResult := db.Find(&first)
	err = firstResult.Error
	require.NoError(t, err)
	require.Len(t, first, 1, "第一次从数据库读取应该返回数据并写入缓存")
	waitForCachedQuery(t, store, firstResult)

	// 关闭数据库访问，强制依赖缓存
	conn.fail = true

	var cached []cacheUser
	err = db.Find(&cached).Error
	require.NoError(t, err, "缓存读取失败时会继续访问数据库导致报错")
	require.Len(t, cached, 1, "第二次命中缓存仍然返回空数据")
}

func waitForCachedQuery(t *testing.T, store cache.Cache, result *gorm.DB) {
	t.Helper()
	dry := result.Session(&gorm.Session{DryRun: true}).Find(&[]cacheUser{})
	require.NoError(t, dry.Error)
	key := (&GormCache{}).key(dry)
	// Ristretto 的写入异步可见，先确认入缓存再关闭数据库或验证失效。
	require.Eventually(t, func() bool {
		_, err := store.Get(context.Background(), key)
		return err == nil
	}, time.Second, time.Millisecond)
}

func TestCacheInvalidatesOverriddenTable(t *testing.T) {
	db := openTestDB(t)
	store, err := cache.New(nil)
	require.NoError(t, err)
	require.NoError(t, db.Use(NewGormCache(store)))
	require.NoError(t, db.Table("other_users").AutoMigrate(&cacheUser{}))
	require.NoError(t, db.Table("other_users").Create(&cacheUser{ID: 1, Name: "old"}).Error)
	var first []cacheUser
	result := db.Table("other_users").Find(&first)
	require.NoError(t, result.Error)
	waitForCachedQuery(t, store, result)
	require.NoError(t, db.Model(&cacheUser{}).Table("other_users AS u").Where("u.id = ?", 1).Update("name", "new").Error)
	var updated []cacheUser
	require.NoError(t, db.Table("other_users").Find(&updated).Error)
	require.Equal(t, "new", updated[0].Name)
}

func TestCacheUnsupportedDialectBypassesStore(t *testing.T) {
	db := openTestDB(t)
	db.Dialector = db.Dialector.(dialectAlias).Dialector
	require.NoError(t, db.AutoMigrate(&cacheUser{}))
	require.NoError(t, db.Create(&cacheUser{ID: 1, Name: "Tom"}).Error)
	require.NoError(t, db.Use(NewGormCache(nil)))
	var users []cacheUser
	require.NoError(t, db.Find(&users).Error)
	require.Len(t, users, 1)
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
