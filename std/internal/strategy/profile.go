package strategy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ichaly/ideabase/log"
	"github.com/ichaly/ideabase/utl"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// ProfileLoadStrategy 配置文件环境加载策略
type ProfileLoadStrategy struct {
	basePath   string
	baseName   string
	configType string
	delim      string
}

// NewProfileLoadStrategy 创建配置文件环境加载策略
func NewProfileLoadStrategy(configFile, configType, delim string) *ProfileLoadStrategy {
	// 如果未指定配置文件，则跳过
	if configFile == "" {
		return &ProfileLoadStrategy{}
	}

	// 如果未指定配置类型，则从文件扩展名推断
	if configType == "" {
		ext := filepath.Ext(configFile)
		configType = strings.TrimPrefix(ext, ".")
	}

	// 获取配置文件路径和名称
	path := filepath.Dir(configFile)
	ext := filepath.Ext(configFile)
	name := strings.TrimSuffix(filepath.Base(configFile), ext)

	return &ProfileLoadStrategy{
		basePath:   path,
		baseName:   name,
		configType: configType,
		delim:      delim,
	}
}

// Load 实现LoadStrategy接口，加载profile配置
func (my *ProfileLoadStrategy) Load(k *koanf.Koanf) error {
	// 如果没有基本信息，则跳过
	if my.basePath == "" || my.baseName == "" {
		return nil
	}

	// 获取激活的profiles
	profiles := getActiveProfiles(k)
	if len(profiles) == 0 {
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

	// 合并每个profile的配置
	for _, profile := range profiles {
		if profile == "" {
			continue
		}

		// 构建profile配置文件路径
		profileFilePath := filepath.Join(my.basePath, utl.JoinString(my.baseName, "-", profile, ".", my.configType))

		// 检查文件是否存在
		if _, err := os.Stat(profileFilePath); os.IsNotExist(err) {
			log.Debug().Str("profile", profile).Str("file", profileFilePath).Msg("配置文件不存在，跳过")
			continue
		}

		// 合并profile配置
		if err := k.Load(file.Provider(profileFilePath), parser); err != nil {
			return fmt.Errorf("合并profile配置文件失败: %w", err)
		}

		log.Info().Str("profile", profile).Str("file", profileFilePath).Msg("配置文件已合并")
	}

	return nil
}

// GetName 返回策略名称
func (my *ProfileLoadStrategy) GetName() string {
	return "Profile配置"
}

// getActiveProfiles 获取激活的profiles
func getActiveProfiles(k *koanf.Koanf) []string {
	var profiles []string

	// 添加profiles.active中指定的profiles
	activeProfiles := strings.Split(k.String("profiles.active"), ",")
	for _, p := range activeProfiles {
		if p = strings.TrimSpace(p); p != "" {
			profiles = append(profiles, p)
		}
	}

	// 添加mode作为profile
	if mode := k.String("mode"); mode != "" {
		profiles = append(profiles, mode)
	}

	return profiles
}
