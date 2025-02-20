package log

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestNewLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(WithOutput(&buf), WithLevel(DebugLevel))

	// 测试不同级别的日志
	logger.Debug().Msg("debug message")
	output := buf.String()
	fmt.Printf("Debug output: %q\n", output)
	var logMap map[string]interface{}
	if err := json.Unmarshal([]byte(output), &logMap); err != nil {
		t.Errorf("Failed to parse log output: %v", err)
	}
	if logMap["level"] != "debug" || logMap["message"] != "debug message" {
		t.Errorf("Expected debug message in log output, got: %q", output)
	}

	buf.Reset()
	logger.Info().Msg("info message")
	output = buf.String()
	fmt.Printf("Info output: %q\n", output)
	if err := json.Unmarshal([]byte(output), &logMap); err != nil {
		t.Errorf("Failed to parse log output: %v", err)
	}
	if logMap["level"] != "info" || logMap["message"] != "info message" {
		t.Errorf("Expected info message in log output, got: %q", output)
	}

	// 测试字段
	buf.Reset()
	logger.Info().
		Str("string", "value").
		Int("int", 123).
		Msg("message with fields")
	output = buf.String()
	fmt.Printf("Fields output: %q\n", output)
	if err := json.Unmarshal([]byte(output), &logMap); err != nil {
		t.Errorf("Failed to parse log output: %v", err)
	}
	if logMap["level"] != "info" ||
		logMap["string"] != "value" ||
		logMap["int"].(float64) != 123 ||
		logMap["message"] != "message with fields" {
		t.Errorf("Expected fields in log output, got: %q", output)
	}
}

func TestRotateLogger(t *testing.T) {
	// 创建临时目录
	tmpDir, err := os.MkdirTemp("", "logger_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	logFile := filepath.Join(tmpDir, "test.log")

	// 创建轮转日志
	logger := NewRotateLogger(
		WithFilename(logFile),
		WithMaxSize(1),
		WithMaxAge(1),
		WithMaxBackups(1),
	)

	// 写入一些日志
	for i := 0; i < 100; i++ {
		logger.Info().Msg("test rotate log message")
	}

	// 检查日志文件是否存在
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		t.Error("Expected log file to exist")
	}
}

func TestGlobalLogger(t *testing.T) {
	var buf bytes.Buffer
	SetDefault(NewLogger(WithOutput(&buf), WithLevel(InfoLevel)))

	// 测试全局方法
	Info().Msg("global info message")
	output := buf.String()
	fmt.Printf("Global info output: %q\n", output)
	var logMap map[string]interface{}
	if err := json.Unmarshal([]byte(output), &logMap); err != nil {
		t.Errorf("Failed to parse log output: %v", err)
	}
	if logMap["level"] != "info" || logMap["message"] != "global info message" {
		t.Errorf("Expected info message in global log output, got: %q", output)
	}

	// 测试日志级别
	buf.Reset()
	Debug().Msg("global debug message")
	output = buf.String()
	fmt.Printf("Global debug output: %q\n", output)
	if output != "" {
		t.Errorf("Debug message should not appear in InfoLevel logger, got: %q", output)
	}
}

func TestLoggerWith(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(WithOutput(&buf))

	// 测试 With 上下文
	contextLogger := logger.With().
		Str("component", "test").
		Logger()

	contextLogger.Info().Msg("context message")
	output := buf.String()
	fmt.Printf("Context output: %q\n", output)
	var logMap map[string]interface{}
	if err := json.Unmarshal([]byte(output), &logMap); err != nil {
		t.Errorf("Failed to parse log output: %v", err)
	}
	if logMap["level"] != "info" ||
		logMap["component"] != "test" ||
		logMap["message"] != "context message" {
		t.Errorf("Expected context field in log output, got: %q", output)
	}
}
