package internal

import (
	"github.com/rs/zerolog"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Rotate 定义日志轮转配置
type Rotate struct {
	// Filename 日志文件路径
	Filename string
	// MaxSize 单个日志文件最大尺寸，单位是MB
	MaxSize int
	// MaxAge 文件最大保存天数
	MaxAge int
	// MaxBackups 最大保留文件数
	MaxBackups int
	// LocalTime 是否使用本地时间
	LocalTime bool
	// Compress 是否压缩旧文件
	Compress bool
	// Daily 是否按天切割
	Daily bool
	// LevelFunc 日志级别转换函数
	LevelFunc LevelFunc
}

// NewRotateLogger 创建一个新的日志轮转器
func NewRotateLogger(r *Rotate) zerolog.Logger {
	w := &lumberjack.Logger{
		Filename:   r.Filename,
		MaxSize:    r.MaxSize,
		MaxAge:     r.MaxAge,
		MaxBackups: r.MaxBackups,
		LocalTime:  r.LocalTime,
		Compress:   r.Compress,
	}

	var l zerolog.Logger
	if r.LevelFunc != nil {
		l = zerolog.New(w).With().Timestamp().Logger().Hook(NewLevelHook(r.LevelFunc))
	} else {
		l = zerolog.New(w).With().Timestamp().Logger()
	}

	return l
}

// DefaultRotateConfig 返回默认的日志轮转配置
func DefaultRotateConfig() *Rotate {
	return &Rotate{
		Filename:   "logs/app.log",
		MaxSize:    100,
		MaxAge:     7,
		MaxBackups: 3,
		LocalTime:  true,
		Compress:   false,
		Daily:      true,
		LevelFunc:  nil,
	}
}
