package metadata

import (
	"github.com/ichaly/ideabase/gql/internal"
	"gorm.io/gorm"
)

// FileLoader 文件元数据加载器
// 实现Loader接口
type FileLoader struct {
	cfg *internal.Config
}

// NewFileLoader 创建文件加载器
func NewFileLoader(cfg *internal.Config) *FileLoader {
	return &FileLoader{cfg: cfg}
}

func (my *FileLoader) Name() string  { return LoaderFile }
func (my *FileLoader) Priority() int { return 80 }

// Support 判断是否支持文件加载（通常总是支持）
func (my *FileLoader) Support(cfg *internal.Config, db *gorm.DB) bool {
	return true
}

// Load 从文件加载元数据
func (my *FileLoader) Load(h Hoster) error {
	// TODO: 实现文件元数据加载逻辑
	return nil
}
