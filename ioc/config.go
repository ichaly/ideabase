package ioc

import (
	"github.com/ichaly/ideabase/std"
	"go.uber.org/fx"
)

// 配置模块
func init() {
	Add(fx.Module("config",
		fx.Provide(
			// 传递 Option 参数,filePath由调fx.Supply方法提供
			fx.Annotate(
				std.WithFilePath,
				fx.ResultTags(`group:"konfigOptions"`),
			),
			fx.Annotate(
				std.NewKonfig,
				fx.ParamTags(`group:"konfigOptions"`),
			),
			std.NewConfig,
		),
	))
}
