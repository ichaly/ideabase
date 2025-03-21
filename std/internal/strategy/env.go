package strategy

import (
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/log"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/v2"
)

// EnvLoadStrategy 环境变量加载策略
type EnvLoadStrategy struct {
	envPrefix string
	delim     string
}

// NewEnvLoadStrategy 创建环境变量加载策略
func NewEnvLoadStrategy(envPrefix, delim string) *EnvLoadStrategy {
	return &EnvLoadStrategy{
		envPrefix: envPrefix,
		delim:     delim,
	}
}

// Load 实现LoadStrategy接口，从环境变量加载配置
func (my *EnvLoadStrategy) Load(k *koanf.Koanf) error {
	// 构建环境变量提供者
	prefix := my.envPrefix + "_"
	callback := func(envKey string) string {
		return strings.Replace(
			strings.ToLower(strings.TrimPrefix(envKey, prefix)),
			"_",
			my.delim,
			-1,
		)
	}

	envProvider := env.Provider(prefix, my.delim, callback)

	// 加载环境变量
	if err := k.Load(envProvider, nil); err != nil {
		return fmt.Errorf("加载环境变量失败: %w", err)
	}

	log.Debug().Str("prefix", my.envPrefix).Msg("环境变量已加载")
	return nil
}

// GetName 返回策略名称
func (my *EnvLoadStrategy) GetName() string {
	return "环境变量"
}
