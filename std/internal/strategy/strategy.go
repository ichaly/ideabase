package strategy

import (
	"github.com/knadh/koanf/v2"
)

// LoadStrategy 定义配置加载策略接口
type LoadStrategy interface {
	// Load 加载配置到koanf实例
	Load(k *koanf.Koanf) error
	// GetName 获取策略名称，用于日志
	GetName() string
}
