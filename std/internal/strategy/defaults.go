package strategy

import (
	"fmt"

	"github.com/ichaly/ideabase/log"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"
)

// DefaultsLoadStrategy 默认值加载策略
type DefaultsLoadStrategy struct {
	defaults map[string]interface{}
	delim    string
}

// NewDefaultsLoadStrategy 创建默认值加载策略
func NewDefaultsLoadStrategy(defaults map[string]interface{}, delim string) *DefaultsLoadStrategy {
	return &DefaultsLoadStrategy{
		defaults: defaults,
		delim:    delim,
	}
}

// Load 实现LoadStrategy接口，加载默认值
func (my *DefaultsLoadStrategy) Load(k *koanf.Koanf) error {
	if len(my.defaults) == 0 {
		return nil
	}

	// 使用 confmap.Provider 加载默认值
	if err := k.Load(confmap.Provider(my.defaults, my.delim), nil); err != nil {
		return fmt.Errorf("加载默认值失败: %w", err)
	}

	log.Debug().Int("count", len(my.defaults)).Msg("默认值已加载")
	return nil
}

// GetName 返回策略名称
func (my *DefaultsLoadStrategy) GetName() string {
	return "默认值"
}
