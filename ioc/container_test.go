package ioc

import (
	"context"
	"testing"

	"github.com/ichaly/ideabase/std"
	_ "github.com/ichaly/ideabase/std/postgres"
	"go.uber.org/fx"
)

func TestOnStopClosesProvidedConnection(t *testing.T) {
	saved := options
	options = nil
	t.Cleanup(func() { options = saved })

	var connection, stopped *std.Connection
	Bind(std.NewConnection, OnStop(func(_ context.Context, db *std.Connection) error {
		stopped = db
		return db.Close()
	}))
	config := &std.Config{}
	config.Database = "postgres://localhost/test?sslmode=disable"
	app := fx.New(fx.Options(options...), fx.Supply(config), fx.NopLogger,
		fx.Invoke(func(db *std.Connection) { connection = db }))
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if connection == nil || stopped != nil {
		t.Fatal("connection must be constructed before its stop callback runs")
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stopped != connection {
		t.Fatal("stop callback did not receive the provided connection")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 未关闭时也不尝试连接真实数据库。
	if err := connection.Pool().PingContext(ctx); err == nil || err.Error() != "sql: database is closed" {
		t.Fatalf("expected a closed shared pool after stopping the container, got %v", err)
	}
}
