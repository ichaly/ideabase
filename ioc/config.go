package ioc

import (
	"github.com/ichaly/ideabase/std"
	"go.uber.org/fx"
)

// 配置模块
func init() {
	Add(fx.Module("config",
		fx.Provide(
			// 通过闭包传递 Option 参数,filePath由调fx.Supply方法提供
			func(filePath string) std.KonfigOption {
				return std.WithFilePath(filePath)
			},
			std.NewKonfig,
			std.NewConfig,
		),
	))
}
