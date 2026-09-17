package mysql

import (
	"strings"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/ichaly/ideabase/std"
)

func TestOpenConnectionMySQLURL(t *testing.T) {
	connection, err := std.OpenConnection("mysql://demo:pass%40word@localhost:3306/demo%2Fapp?charset=latin1&parseTime=true")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if connection.Driver() != "mysql" || connection.Pool().Stats().OpenConnections != 0 {
		t.Fatal("registered MySQL driver did not create a lazy pool with the expected dialect")
	}
	dsn := connection.DSN()
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.User != "demo" || cfg.Passwd != "pass@word" || cfg.Addr != "localhost:3306" || cfg.DBName != "demo/app" || !cfg.ParseTime || !strings.Contains(dsn, "charset=latin1") {
		t.Fatalf("unexpected config: user=%q addr=%q db=%q parseTime=%v", cfg.User, cfg.Addr, cfg.DBName, cfg.ParseTime)
	}
}

func TestToDSNDefaults(t *testing.T) {
	dsn, err := toDSN("mysql://demo:secret@localhost/demo")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dsn, "charset=utf8mb4") || !cfg.ParseTime || cfg.Loc.String() != "Local" {
		t.Fatalf("unexpected defaults: dsn=%q parseTime=%v loc=%v", dsn, cfg.ParseTime, cfg.Loc)
	}
}

func TestToDSNRejectsInvalidQuery(t *testing.T) {
	if _, err := toDSN("mysql://demo:secret@localhost/demo?bad=%zz"); err == nil {
		t.Fatal("toDSN accepted an invalid query")
	}
}
