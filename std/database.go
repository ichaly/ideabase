package std

import (
	"fmt"
	"time"

	"github.com/ichaly/ideabase/std/internal"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func NewDatabase(c *Config, e []interface{}, p []gorm.Plugin) (*gorm.DB, error) {
	prepareStmt := c.Database.Dialect == "mysql" || !c.IsDebug()
	db, err := gorm.Open(
		buildDialect(c.Database),
		&gorm.Config{PrepareStmt: prepareStmt, Logger: logger.Default.LogMode(logger.Info)},
	)
	if err != nil {
		return nil, err
	}
	for _, v := range p {
		err = db.Use(v)
		if err != nil {
			return nil, err
		}
	}
	if c.IsDebug() {
		for _, v := range e {
			tx := db
			err = tx.AutoMigrate(v)
			if err != nil {
				return nil, err
			}
			if err = migrateComment(tx, v); err != nil {
				return nil, err
			}
			if err = migrateEntity(tx, v); err != nil {
				return nil, err
			}
		}
	}
	sqlDb, _ := db.DB()
	sqlDb.SetMaxIdleConns(5)
	sqlDb.SetMaxOpenConns(90)
	sqlDb.SetConnMaxLifetime(5 * time.Minute)
	return db, nil
}

func buildDialect(ds *internal.DataSource) gorm.Dialector {
	args := []interface{}{ds.Username, ds.Password, ds.Host, ds.Port, ds.Name}
	if ds.Dialect == "mysql" {
		return mysql.Open(fmt.Sprintf(
			"%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local", args...,
		))
	} else {
		return postgres.Open(fmt.Sprintf(
			"user=%s password=%s host=%s port=%d dbname=%s TimeZone=Asia/Shanghai", args...,
		))
	}
}
