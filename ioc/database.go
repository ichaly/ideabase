package ioc

import (
	"github.com/ichaly/ideabase/std"
	"go.uber.org/fx"
)

// 数据库模块
func init() {
	Add(fx.Module("database",
		fx.Provide(
			std.NewStorage,
			fx.Annotated{
				Group:  "gorm",
				Target: std.NewSonyFlake,
			},
			fx.Annotated{
				Group:  "gorm",
				Target: std.NewCache,
			},
			fx.Annotate(
				std.NewConnect,
				fx.ParamTags(``, `group:"gorm"`, `group:"entity"`),
			),
		),
	))
}
