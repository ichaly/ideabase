package postgres

import (
	"testing"

	"github.com/ichaly/ideabase/std"
	"github.com/jackc/pgx/v5"
)

func TestNewConnectionPostgresURLs(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"POSTGRES://demo:pass%40word@localhost/app", "postgres://demo:pass%40word@localhost/app"},
		{"postgres://demo:pass%40word@localhost/app", "postgres://demo:pass%40word@localhost/app"},
	} {
		settings := &std.Config{}
		settings.Database = test.input
		connection, err := std.NewConnection(settings)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = connection.Close() })
		if connection.DSN() != test.want || connection.Driver() != "postgres" {
			t.Fatal("registered PostgreSQL driver did not preserve canonical dialect and URL")
		}
		config, err := pgx.ParseConfig(connection.DSN())
		if err != nil {
			t.Fatal(err)
		}
		if config.Password != "pass@word" || config.Host != "localhost" || config.Database != "app" {
			t.Fatal("PostgreSQL URL connection fields were not preserved")
		}
	}
}
