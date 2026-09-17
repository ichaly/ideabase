package postgres

import (
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/std"
	_ "github.com/jackc/pgx/v5/stdlib"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// 使用: import _ "github.com/ichaly/ideabase/std/postgres"
// URL: postgres://user:pass@host:5432/dbname?sslmode=disable
func init() {
	std.RegisterDatabase(std.DatabaseDriver{
		Dialect:   "postgres",
		SQLDriver: "pgx",
		ParseDSN:  normalizeDSN,
		Dialector: func(connection *std.Connection) gorm.Dialector {
			return pgdriver.New(pgdriver.Config{Conn: connection.Pool(), DSN: connection.DSN()})
		},
	}, "postgresql")
}

func normalizeDSN(rawDSN string) (string, error) {
	scheme, rest, ok := strings.Cut(rawDSN, "://")
	if !ok || (!strings.EqualFold(scheme, "postgres") && !strings.EqualFold(scheme, "postgresql")) {
		return "", fmt.Errorf("invalid postgres database DSN")
	}
	return strings.ToLower(scheme) + "://" + rest, nil
}
