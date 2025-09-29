package ioc

import (
	"github.com/ichaly/ideabase/std"
)

var options []Option

func Add(args ...Option) {
	options = append(options, args...)
}

func Get() Option {
	return Options(options...)
}

func init() {
	Add(
		Provide(
			newAdapter,
			std.NewFiber,
			Annotate(
				std.NewHealth,
				As(new(std.Plugin)),
				ResultTags(`group:"plugin"`),
			),
		),
		Invoke(Annotate(std.Bootstrap, ParamTags(`group:"plugin"`, `group:"filter"`))),
	)
}
