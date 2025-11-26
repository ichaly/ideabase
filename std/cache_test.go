package std

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	bigcachelib "github.com/allegro/bigcache/v3"
	gocachelib "github.com/eko/gocache/lib/v4/cache"
	gocachestore "github.com/eko/gocache/store/bigcache/v4"
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

func TestCacheSecondLoadShouldReturnData(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	big, err := bigcachelib.NewBigCache(bigcachelib.DefaultConfig(10 * time.Minute))
	require.NoError(t, err)

	err = db.Use(NewCache(gocachelib.New[[]byte](
		gocachestore.NewBigcache(big),
	)))
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
