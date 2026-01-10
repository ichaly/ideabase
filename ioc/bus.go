package ioc

import "github.com/ichaly/ideabase/bus"

// 消息总线模块
var (
	_ = Bind(bus.NewBus, In("", `optional:"true"`, `optional:"true"`))
)
