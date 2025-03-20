package std

import (
	"fmt"
	"time"

	"github.com/ichaly/ideabase/std/internal"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func NewConnect(k *Konfig, p []gorm.Plugin, e []interface{}) (*gorm.DB, error) {
	c := &internal.DatabaseConfig{}
	if err := k.Unmarshal(c); err != nil {
		return nil, err
	}
	db, err := gorm.Open(
		buildDialect(&c.DataSource),
		&gorm.Config{PrepareStmt: true, Logger: logger.Default.LogMode(logger.Info)},
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
	if c.Debug {
		for _, v := range e {
			name, desc := "", ""
			if n, ok := v.(schema.Tabler); ok {
				name = n.TableName()
			}
			if n, ok := v.(Description); ok {
				desc = n.Description()
			}
			options := fmt.Sprintf(";comment on table %s is '%s';", name, desc)
			tx := db
			if name != "" && desc != "" {
				tx = db.Set("gorm:table_options", options)
			}
			err = tx.AutoMigrate(v)
			if err != nil {
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
