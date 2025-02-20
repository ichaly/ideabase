package log

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

type Level = zerolog.Level

const (
	// TraceLevel 跟踪级别
	TraceLevel = zerolog.TraceLevel
	// DebugLevel 调试级别
	DebugLevel = zerolog.DebugLevel
	// InfoLevel 信息级别
	InfoLevel = zerolog.InfoLevel
	// WarnLevel 警告级别
	WarnLevel = zerolog.WarnLevel
	// ErrorLevel 错误级别
	ErrorLevel = zerolog.ErrorLevel
	// FatalLevel 致命错误级别
	FatalLevel = zerolog.FatalLevel
	// PanicLevel panic级别
	PanicLevel = zerolog.PanicLevel
	// NoLevel 无级别
	NoLevel = zerolog.NoLevel
	// Disabled 禁用日志
	Disabled = zerolog.Disabled
)

// Logger 日志记录器
type Logger struct {
	l zerolog.Logger
}

// NewLogger 创建新的日志记录器
func NewLogger(ops ...LoggerOption) *Logger {
	zerolog.TimeFieldFormat = time.DateTime
	zerolog.LevelFieldName = "level"
	zerolog.MessageFieldName = "message"
	zerolog.TimestampFieldName = "time"

	console := zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: time.DateTime,
		NoColor:    false,
	}

	console.FormatTimestamp = func(i interface{}) string {
		return fmt.Sprintf("[%s] ", i)
	}

	console.FormatLevel = func(i interface{}) string {
		return strings.ToUpper(fmt.Sprintf("| %-6s|", i))
	}

	console.FormatMessage = func(i interface{}) string {
		if i == nil {
			return ""
		}
		return fmt.Sprintf("%s", i)
	}

	console.FormatFieldName = func(i interface{}) string {
		return fmt.Sprintf(" %s=", i)
	}

	console.FormatFieldValue = func(i interface{}) string {
		return fmt.Sprintf("%v", i)
	}

	l := zerolog.New(console).With().Timestamp().Logger()
	for _, o := range ops {
		l = o(l)
	}

	return &Logger{l: l}
}

// SetLevel 设置日志级别
func (my *Logger) SetLevel(level Level) {
	my.l = my.l.Level(level)
}

// With 返回一个带有上下文字段的新Logger
func (my *Logger) With() zerolog.Context {
	return my.l.With()
}

// Trace 返回Trace级别的日志事件
func (my *Logger) Trace() *zerolog.Event {
	return my.l.Trace()
}

// Debug 返回Debug级别的日志事件
func (my *Logger) Debug() *zerolog.Event {
	return my.l.Debug()
}

// Info 返回Info级别的日志事件
func (my *Logger) Info() *zerolog.Event {
	return my.l.Info()
}

// Warn 返回Warn级别的日志事件
func (my *Logger) Warn() *zerolog.Event {
	return my.l.Warn()
}

// Error 返回Error级别的日志事件
func (my *Logger) Error() *zerolog.Event {
	return my.l.Error()
}

// Fatal 返回Fatal级别的日志事件
func (my *Logger) Fatal() *zerolog.Event {
	return my.l.Fatal()
}

// Panic 返回Panic级别的日志事件
func (my *Logger) Panic() *zerolog.Event {
	return my.l.Panic()
}

// 全局默认logger实例
var std = NewLogger(WithOutput(os.Stderr), WithLevel(InfoLevel))

// Default 返回默认logger实例
func Default() *Logger { return std }

// SetDefault 设置默认logger实例
func SetDefault(l *Logger) { std = l }

// SetLevel 设置默认logger的日志级别
func SetLevel(level Level) { std.SetLevel(level) }

// 全局方法
func Trace() *zerolog.Event { return std.Trace() }
func Debug() *zerolog.Event { return std.Debug() }
func Info() *zerolog.Event  { return std.Info() }
func Warn() *zerolog.Event  { return std.Warn() }
func Error() *zerolog.Event { return std.Error() }
func Fatal() *zerolog.Event { return std.Fatal() }
func Panic() *zerolog.Event { return std.Panic() }
