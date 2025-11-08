package ioc

import "github.com/ichaly/ideabase/std"

// 配置模块
var (
	_ = Bind(std.WithFilePath, Out("konfigOptions"))
	_ = Bind(std.NewKonfig, In("konfigOptions"))
	_ = Bind(std.NewConfig)
)
