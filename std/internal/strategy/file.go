package strategy

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ichaly/ideabase/log"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// FileLoadStrategy 文件加载策略
type FileLoadStrategy struct {
	configFile string
	configType string
	delim      string
}

// NewFileLoadStrategy 创建文件加载策略
func NewFileLoadStrategy(configFile, configType, delim string) *FileLoadStrategy {
	// 如果未指定配置类型，则从文件扩展名推断
	if configType == "" && configFile != "" {
		ext := filepath.Ext(configFile)
		configType = strings.TrimPrefix(ext, ".")
	}

	return &FileLoadStrategy{
		configFile: configFile,
		configType: configType,
		delim:      delim,
	}
}

// Load 实现LoadStrategy接口，从文件加载配置
func (my *FileLoadStrategy) Load(k *koanf.Koanf) error {
	// 如果没有配置文件，则跳过
	if my.configFile == "" {
		return nil
	}

	// 选择解析器
	var parser koanf.Parser
	switch my.configType {
	case "yaml", "yml":
		parser = yaml.Parser()
	default:
		return fmt.Errorf("不支持的配置文件类型: %s", my.configType)
	}

	// 加载配置文件
	if err := k.Load(file.Provider(my.configFile), parser); err != nil {
		return fmt.Errorf("加载配置文件失败: %w", err)
	}

	log.Info().Str("file", my.configFile).Msg("配置文件已加载")
	return nil
}

// GetName 返回策略名称
func (my *FileLoadStrategy) GetName() string {
	return "配置文件"
}
