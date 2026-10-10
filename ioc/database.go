package ioc

import (
	"context"

	"github.com/ichaly/ideabase/std"
	"github.com/ichaly/ideabase/std/cache"
	"github.com/ichaly/ideabase/std/event"
)

var (
	_ = Bind(std.NewNats)
	_ = Bind(std.NewRedis)
	_ = Bind(cache.New, In(`optional:"true"`))
	_ = Bind(event.New, In(`optional:"true"`, `optional:"true"`))
	_ = Bind(std.NewGormCache, Out("gorm"))
	_ = Bind(std.NewSonyFlake, Out("gorm"))
	_ = Bind(std.NewAudited, Out("gorm"))
	_ = Bind(std.NewConnection, OnStop(func(_ context.Context, db *std.Connection) error {
		return db.Close()
	}))
	_ = Bind(std.NewDatabase, In("entity", "gorm"))
)
