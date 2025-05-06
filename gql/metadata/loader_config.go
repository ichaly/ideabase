package metadata

import (
	"github.com/ichaly/ideabase/gql/internal"
	"gorm.io/gorm"
)

// ConfigLoader 配置元数据加载器
// 实现Loader接口
type ConfigLoader struct {
	cfg *internal.Config
}

// NewConfigLoader 创建配置加载器
func NewConfigLoader(cfg *internal.Config) *ConfigLoader {
	return &ConfigLoader{cfg: cfg}
}

func (my *ConfigLoader) Name() string  { return LoaderConfig }
func (my *ConfigLoader) Priority() int { return 100 }

// Support 判断是否支持配置加载（通常总是支持）
func (my *ConfigLoader) Support(cfg *internal.Config, db *gorm.DB) bool {
	return true
}

// Load 从配置加载元数据
func (my *ConfigLoader) Load(h Hoster) error {
	// TODO: 实现配置元数据加载逻辑
	return nil
}
