package ioc

import (
	"go.uber.org/fx"

	"github.com/ichaly/ideabase/std"
)

var Dependencies = fx.Options(
	fx.Provide(fx.Annotated{
		Group:  "gorm",
		Target: std.NewSonyFlake,
	}),
)
