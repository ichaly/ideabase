package logging

import (
	"io"

	"github.com/ichaly/ideabase/logging/internal"
	"github.com/rs/zerolog"
)

// LoggerOption 定义日志选项函数类型
type LoggerOption func(zerolog.Logger) zerolog.Logger

// RotateOption 定义日志轮转选项函数类型
type RotateOption func(*internal.Rotate)

// WithOutput 设置日志输出目标
func WithOutput(out io.Writer) LoggerOption {
	return func(l zerolog.Logger) zerolog.Logger {
		return l.Output(out)
	}
}

// WithLevel 设置日志级别
func WithLevel(level Level) LoggerOption {
	return func(l zerolog.Logger) zerolog.Logger {
		return l.Level(level)
	}
}

// WithLevelFunc 设置日志级别函数
func WithLevelFunc(fn func(string) Level) RotateOption {
	return func(r *internal.Rotate) {
		r.LevelFunc = fn
	}
}

// WithFilename 设置日志文件名
func WithFilename(filename string) RotateOption {
	return func(r *internal.Rotate) {
		r.Filename = filename
	}
}

// WithMaxAge 设置日志最大保存时间（天）
func WithMaxAge(maxAge int) RotateOption {
	return func(r *internal.Rotate) {
		r.MaxAge = maxAge
	}
}

// WithMaxSize 设置单个日志文件最大尺寸（MB）
func WithMaxSize(maxSize int) RotateOption {
	return func(r *internal.Rotate) {
		r.MaxSize = maxSize
	}
}

// WithMaxBackups 设置最大备份文件数
func WithMaxBackups(maxBackups int) RotateOption {
	return func(r *internal.Rotate) {
		r.MaxBackups = maxBackups
	}
}

// UseCompress 是否压缩旧日志文件
func UseCompress(compress bool) RotateOption {
	return func(r *internal.Rotate) {
		r.Compress = compress
	}
}

// UseLocalTime 是否使用本地时间
func UseLocalTime(localTime bool) RotateOption {
	return func(r *internal.Rotate) {
		r.LocalTime = localTime
	}
}

// UseDaily 是否按天切割日志
func UseDaily(daily bool) RotateOption {
	return func(r *internal.Rotate) {
		r.Daily = daily
	}
}

// NewRotateLogger 创建一个新的日志轮转记录器
func NewRotateLogger(ops ...RotateOption) *Logger {
	r := internal.DefaultRotateConfig()
	for _, o := range ops {
		o(r)
	}
	return &Logger{l: internal.NewRotateLogger(r)}
}
