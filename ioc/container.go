package ioc

import (
	"github.com/ichaly/ideabase/std"
	"go.uber.org/fx"
)

var options []fx.Option

func Add(args ...fx.Option) {
	options = append(options, args...)
}

func Get() fx.Option {
	return fx.Options(options...)
}

func init() {
	Add(
		fx.Provide(std.NewFiber),
		fx.Provide(newAdapter),
		fx.Invoke(fx.Annotate(std.Bootstrap, fx.ParamTags(`group:"plugin"`, `group:"filter"`))),
	)
}
