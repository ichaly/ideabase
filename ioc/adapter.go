package ioc

import (
	"context"

	"github.com/ichaly/ideabase/std"
	"go.uber.org/fx"
)

type fxAdapter struct {
	fx fx.Lifecycle
}

func newAdapter(fx fx.Lifecycle) std.Lifecycle {
	return &fxAdapter{fx: fx}
}

func (my *fxAdapter) Append(start, stop func(context.Context) error) {
	my.fx.Append(fx.Hook{
		OnStart: start,
		OnStop:  stop,
	})
}