package std

import (
	"fmt"
	"net/url"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Dialector 数据库方言工厂函数
type Dialector func(url string) gorm.Dialector

var dialectors = map[string]Dialector{}

// RegisterDatabase 注册数据库方言（由 std/postgres、std/mysql 等子包在 init() 中调用）
func RegisterDatabase(scheme string, d Dialector) {
	dialectors[scheme] = d
}

// NewDatabase 根据 Config URL 创建数据库连接
func NewDatabase(e []interface{}, p []gorm.Plugin, c *Config) (*gorm.DB, error) {
	u, err := url.Parse(c.Database)
	if err != nil {
		return nil, fmt.Errorf("database: invalid url: %w", err)
	}
	d, ok := dialectors[u.Scheme]
	if !ok {
		return nil, fmt.Errorf("database driver '%s' not registered", u.Scheme)
	}
	db, err := gorm.Open(d(c.Database), &gorm.Config{
		PrepareStmt: u.Scheme == "mysql" || !c.IsDebug(),
		Logger:      logger.Default.LogMode(logger.Info),
	})
	if err != nil {
		return nil, err
	}
	for _, v := range p {
		if err = db.Use(v); err != nil {
			return nil, err
		}
	}
	if c.IsDebug() {
		for _, v := range e {
			tx := db
			if !skipAutoMigrate(v) {
				if err = tx.AutoMigrate(v); err != nil {
					return nil, err
				}
			}
			// 先 migrateEntity 让 NoAutoMigrate 实体在 Migrate 钩子里完成建表，
			// 再 migrateComment 写表注释，避免分区表场景下 COMMENT 时表尚未存在。
			if err = migrateEntity(tx, v); err != nil {
				return nil, err
			}
			if err = migrateComment(tx, v); err != nil {
				return nil, err
			}
		}
	}
	sqlDb, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("database: failed to get sql.DB: %w", err)
	}
	sqlDb.SetMaxIdleConns(5)
	sqlDb.SetMaxOpenConns(90)
	sqlDb.SetConnMaxLifetime(5 * time.Minute)
	return db, nil
}
