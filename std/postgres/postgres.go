package postgres

import (
	"database/sql"
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
	std.RegisterDatabase("postgres", std.Dialector{
		Open: openConnection,
		Gorm: func(connection *std.Connection) gorm.Dialector {
			return pgdriver.New(pgdriver.Config{Conn: connection.Pool(), DSN: connection.DSN()})
		},
	}, "postgresql")
}

func openConnection(rawDSN string) (*sql.DB, string, error) {
	dsn, err := normalizeDSN(rawDSN)
	if err != nil {
		return nil, "", err
	}
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, "", err
	}
	return pool, dsn, nil
}

func normalizeDSN(rawDSN string) (string, error) {
	scheme, rest, ok := strings.Cut(rawDSN, "://")
	if !ok || (!strings.EqualFold(scheme, "postgres") && !strings.EqualFold(scheme, "postgresql")) {
		return "", fmt.Errorf("invalid postgres database DSN")
	}
	return strings.ToLower(scheme) + "://" + rest, nil
}
