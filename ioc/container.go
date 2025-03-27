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
	Add(fx.Invoke(std.Bootstrap))
}
