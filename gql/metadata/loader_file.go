package metadata

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/log"
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
// 1. 计算文件路径
// 2. 读取文件内容
// 3. 反序列化为临时结构
// 4. 遍历meta.Nodes，处理字段索引和多key索引
// 5. 注入Hoster并设置版本号
func (my *FileLoader) Load(h Hoster) error {
	// 1. 计算文件路径，格式为 metadata.{mode}.json
	mode := my.cfg.Mode
	name := fmt.Sprintf("metadata.%s.json", mode)
	path := filepath.Join(my.cfg.Root, "cfg", name)

	log.Info().Str("file", path).Msg("开始从文件加载元数据")

	// 2. 读取文件内容
	data, err := os.ReadFile(path)
	if err != nil {
		log.Error().Err(err).Str("file", path).Msg("读取文件失败")
		return fmt.Errorf("读取文件失败: %w", err)
	}

	// 3. 反序列化为临时结构体，包含所有类节点和版本号
	var meta struct {
		Nodes   map[string]*internal.Class `json:"nodes"`
		Version string                     `json:"version"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		log.Error().Err(err).Str("file", path).Msg("解析JSON失败")
		return fmt.Errorf("解析JSON失败: %w", err)
	}

	// 5. 设置元数据版本号，便于后续追踪和一致性校验
	h.SetVersion(meta.Version)

	// 4. 遍历所有主节点，处理字段名/列名索引和多key索引
	for className, class := range meta.Nodes {
		// 只处理主类名（key与类名一致）
		if className == class.Name {
			// 初始化字段映射，支持字段名和列名双重索引
			fields := make(map[string]*internal.Field)
			for fieldName, field := range class.Fields {
				fields[fieldName] = field
				// 如果列名与字段名不同，添加列名索引，便于通过列名查找字段
				if field.Column != "" && field.Column != fieldName {
					fields[field.Column] = field
				}
			}
			class.Fields = fields
			// 添加类名索引
			_ = h.PutNode(class)
		}
	}
	log.Info().Int("classes", len(meta.Nodes)).Msg("从文件加载元数据完成")
	return nil
}
