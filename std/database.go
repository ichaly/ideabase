package std

import (
	"database/sql"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Dialector 负责打开底层连接，以及在共享池上创建 GORM 方言。
type Dialector struct {
	Open func(string) (*sql.DB, string, error)
	Gorm func(*Connection) gorm.Dialector
	name string
}

var dialectors = map[string]Dialector{}

// RegisterDatabase 在启动时注册驱动及 URL 协议别名。
func RegisterDatabase(name string, d Dialector, aliases ...string) {
	d.name = strings.ToLower(name)
	dialectors[d.name] = d
	for _, alias := range aliases {
		dialectors[strings.ToLower(alias)] = d
	}
}

// NewDatabase 在共享池上装配 GORM，不创建或接管连接池。
func NewDatabase(e []any, p []gorm.Plugin, c *Config, connection *Connection) (*gorm.DB, error) {
	d, ok := dialectors[connection.Driver()]
	if !ok {
		return nil, fmt.Errorf("database driver '%s' not registered", connection.Driver())
	}
	db, err := gorm.Open(d.Gorm(connection), &gorm.Config{
		PrepareStmt: connection.Driver() == "mysql" || !c.IsDebug(),
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
