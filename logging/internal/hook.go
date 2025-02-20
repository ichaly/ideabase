package internal

import "github.com/rs/zerolog"

// LevelFunc 定义日志级别转换函数类型
type LevelFunc func(string) zerolog.Level

// LevelHook 实现日志级别转换钩子
type LevelHook struct {
	levelFunc LevelFunc
}

// NewLevelHook 创建新的日志级别转换钩子
func NewLevelHook(fn LevelFunc) *LevelHook {
	return &LevelHook{levelFunc: fn}
}

// Run 实现 zerolog.Hook 接口
func (my *LevelHook) Run(e *zerolog.Event, level zerolog.Level, msg string) {
	if my.levelFunc != nil {
		newLevel := my.levelFunc(msg)
		if newLevel != level {
			e.Str("original_level", level.String())
		}
	}
}
