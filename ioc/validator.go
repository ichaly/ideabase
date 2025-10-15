package ioc

import (
	"github.com/ichaly/ideabase/std"
	"go.uber.org/fx"
)

// 校验器模块
func init() {
	Add(fx.Module("validator",
		fx.Provide(
			std.NewValidator,
		),
	))
}
