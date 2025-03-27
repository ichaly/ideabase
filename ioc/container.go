package ioc

import (
	"go.uber.org/fx"
)

var options []fx.Option

func Add(args ...fx.Option) {
	options = append(options, args...)
}

func Get() fx.Option {
	return fx.Options(options...)
}
