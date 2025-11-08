package ioc

import "github.com/ichaly/ideabase/std"

// 数据库模块
var (
	_ = Bind(std.NewStorage)
	_ = Bind(std.NewSonyFlake, Out("gorm"))
	_ = Bind(std.NewCache, Out("gorm"))
	_ = Bind(std.NewAudited, Out("gorm"))
	_ = Bind(std.NewDatabase, In("", "entity", "gorm"))
)
