package std

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Dialector 数据库方言工厂函数
type Dialector func(connection *Connection) gorm.Dialector

// DatabaseDriver 将连接串解析和 GORM 装配留在对应驱动包中。
type DatabaseDriver struct {
	Dialect     string
	SQLDriver   string
	ParseDSN    func(string) (string, error)
	Dialector   Dialector
	PrepareStmt bool
}

var databaseDrivers = map[string]DatabaseDriver{}

// RegisterDatabase 在启动时注册驱动及 URL 协议别名。
func RegisterDatabase(driver DatabaseDriver, aliases ...string) {
	driver.Dialect = strings.ToLower(driver.Dialect)
	databaseDrivers[driver.Dialect] = driver
	for _, alias := range aliases {
		databaseDrivers[strings.ToLower(alias)] = driver
	}
}

// NewDatabase 在共享池上装配 GORM，不创建或接管连接池。
func NewDatabase(e []interface{}, p []gorm.Plugin, c *Config, connection *Connection) (*gorm.DB, error) {
	driver, ok := databaseDrivers[connection.Driver()]
	if !ok {
		return nil, fmt.Errorf("database driver '%s' not registered", connection.Driver())
	}
	db, err := gorm.Open(driver.Dialector(connection), &gorm.Config{
		PrepareStmt: driver.PrepareStmt || !c.IsDebug(),
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
	return db, nil
}
