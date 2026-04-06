package postgres

import (
	"github.com/ichaly/ideabase/std"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// 使用: import _ "github.com/ichaly/ideabase/std/postgres"
// URL: postgres://user:pass@host:5432/dbname?sslmode=disable
func init() {
	std.RegisterDatabase("postgres", func(url string) gorm.Dialector {
		return pgdriver.Open(url)
	})
}
