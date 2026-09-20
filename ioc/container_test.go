package ioc

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ichaly/ideabase/std"
	_ "github.com/ichaly/ideabase/std/postgres"
	"go.uber.org/fx"
)

func TestOutIgnoresTrailingError(t *testing.T) {
	failure := errors.New("constructor failed")
	for _, tc := range []struct {
		name      string
		ctor      any
		tags      []string
		number    bool
		wantError error
	}{
		{"single", func() (string, error) { return "value", nil }, []string{"values"}, false, nil},
		{"legacy single", func() (string, error) { return "value", nil }, []string{"values", ""}, false, nil},
		{"multiple", func() (string, int, error) { return "value", 7, nil }, []string{"values", "numbers"}, true, nil},
		{"untagged value", func() (int, string, error) { return 7, "value", nil }, []string{"", "values"}, false, nil},
		{"legacy multiple", func() (string, int, error) { return "value", 7, nil }, []string{"values", "numbers", ""}, true, nil},
		{"without error", func() string { return "value" }, []string{"values"}, false, nil},
		{"constructor failure", func() (string, error) { return "", failure }, []string{"values"}, false, failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := options
			options = nil
			t.Cleanup(func() { options = saved })
			Bind(tc.ctor, Out(tc.tags...))
			called := false
			app := fx.New(fx.Options(options...), fx.NopLogger, fx.Invoke(fx.Annotate(func(values []string, numbers []int) {
				called = true
				if len(values) != 1 || values[0] != "value" {
					t.Fatalf("unexpected group: %v", values)
				}
				if tc.number && (len(numbers) != 1 || numbers[0] != 7) {
					t.Fatalf("unexpected second group: %v", numbers)
				}
			}, fx.ParamTags(`group:"values"`, `group:"numbers"`))))
			if !errors.Is(app.Err(), tc.wantError) {
				t.Fatalf("container error = %v, want %v", app.Err(), tc.wantError)
			}
			if called != (tc.wantError == nil) {
				t.Fatalf("consumer called = %t", called)
			}
		})
	}
}

func TestOutRejectsInvalidResultTags(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctor any
		tags []string
	}{
		{"missing value tag", func() (string, int, error) { return "", 0, nil }, []string{"values"}},
		{"extra tag", func() (string, error) { return "", nil }, []string{"values", "", ""}},
		{"tag on error", func() (string, error) { return "", nil }, []string{"values", "errors"}},
		{"no trailing error", func() string { return "" }, []string{"values", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected invalid result tags to panic")
				}
			}()
			collectAnnotations(reflect.TypeOf(tc.ctor), true, "Bind", Out(tc.tags...))
		})
	}
}

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
