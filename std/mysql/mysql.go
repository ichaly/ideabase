package mysql

import (
	"fmt"
	"net/url"

	"github.com/ichaly/ideabase/std"
	mydriver "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// 使用: import _ "github.com/ichaly/ideabase/std/mysql"
// URL: mysql://user:pass@host:3306/dbname?charset=utf8mb4&parseTime=True
func init() {
	std.RegisterDatabase("mysql", func(rawURL string) gorm.Dialector {
		return mydriver.Open(toDSN(rawURL))
	})
}

// toDSN 将 mysql://user:pass@host:port/db?params 转为 MySQL DSN 格式
func toDSN(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	pass, _ := u.User.Password()
	dsn := fmt.Sprintf("%s:%s@tcp(%s)%s", u.User.Username(), pass, u.Host, u.Path)
	if u.RawQuery != "" {
		dsn += "?" + u.RawQuery
	} else {
		dsn += "?charset=utf8mb4&parseTime=True&loc=Local"
	}
	return dsn
}
