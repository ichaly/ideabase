package ioc

import (
	"go.uber.org/fx"

	"github.com/ichaly/ideabase/std"
)

var options []fx.Option

func init() {
	Add(
		fx.Provide(
			fx.Annotated{
				Group:  "gorm",
				Target: std.NewSonyFlake,
			},
		),
	)
}

func Add(args ...fx.Option) {
	options = append(options, args...)
}

func Get() fx.Option {
	return fx.Options(options...)
}
