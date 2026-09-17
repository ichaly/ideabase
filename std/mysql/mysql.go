package mysql

import (
	"fmt"
	"net/url"
	"strings"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/ichaly/ideabase/std"
	mydriver "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// 使用: import _ "github.com/ichaly/ideabase/std/mysql"
// URL: mysql://user:pass@host:3306/dbname?charset=utf8mb4&parseTime=True
func init() {
	std.RegisterDatabase(std.DatabaseDriver{
		Dialect:     "mysql",
		SQLDriver:   "mysql",
		ParseDSN:    toDSN,
		PrepareStmt: true,
		Dialector: func(connection *std.Connection) gorm.Dialector {
			return mydriver.New(mydriver.Config{Conn: connection.Pool(), DSN: connection.DSN()})
		},
	})
}

// toDSN 将 mysql://user:pass@host:port/db?params 转为 MySQL DSN 格式。
func toDSN(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid mysql database DSN")
	}
	config := mysql.NewConfig()
	if u.User != nil {
		config.User = u.User.Username()
		config.Passwd, _ = u.User.Password()
	}
	config.Net = "tcp"
	config.Addr = u.Host
	config.DBName = strings.TrimPrefix(u.Path, "/")
	if u.RawQuery == "" {
		config.Params = map[string]string{
			"charset":   "utf8mb4",
			"parseTime": "True",
			"loc":       "Local",
		}
	} else {
		values, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return "", fmt.Errorf("invalid mysql database DSN query")
		}
		config.Params = make(map[string]string)
		for key, items := range values {
			if len(items) > 0 {
				config.Params[key] = items[len(items)-1]
			}
		}
	}
	return config.FormatDSN(), nil
}
