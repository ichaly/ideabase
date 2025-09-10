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
		fx.Provide(
			newAdapter,
			std.NewFiber,
			fx.Annotate(
				std.NewHealth,
				fx.As(new(std.Plugin)),
				fx.ResultTags(`group:"plugin"`),
			),
		),
		fx.Invoke(fx.Annotate(std.Bootstrap, fx.ParamTags(`group:"plugin"`, `group:"filter"`))),
	)
}
