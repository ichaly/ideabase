package strategy

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ichaly/ideabase/log"
	"github.com/ichaly/ideabase/utl"
	"github.com/joho/godotenv"
	"github.com/knadh/koanf/v2"
)

// DotEnvLoadStrategy .env文件加载策略
type DotEnvLoadStrategy struct{}

// NewDotEnvLoadStrategy 创建.env文件加载策略
func NewDotEnvLoadStrategy() *DotEnvLoadStrategy {
	return &DotEnvLoadStrategy{}
}

// Load 实现LoadStrategy接口，加载.env文件
func (my *DotEnvLoadStrategy) Load(k *koanf.Koanf) error {
	envFile := filepath.Join(utl.Root(), ".env")

	// 检查文件是否存在
	if _, err := os.Stat(envFile); os.IsNotExist(err) {
		// .env 文件不存在,跳过加载
		return nil
	}

	// 加载 .env 文件
	if err := godotenv.Load(envFile); err != nil {
		return fmt.Errorf("加载.env文件失败: %w", err)
	}

	log.Debug().Str("file", envFile).Msg(".env文件已加载")
	return nil
}

// GetName 返回策略名称
func (my *DotEnvLoadStrategy) GetName() string {
	return ".env文件"
}
