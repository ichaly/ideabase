package metadata

import (
	"github.com/ichaly/ideabase/gql/internal"
	"gorm.io/gorm"
)

// Loader名称常量
const (
	LoaderFile   = "file"
	LoaderPgsql  = "pgsql"
	LoaderMysql  = "mysql"
	LoaderConfig = "config"
)

// Hoster 定义元数据承载者接口
// 负责节点的添加和获取
type Hoster interface {
	PutNode(node *internal.Class) error
	GetNode(name string) (*internal.Class, bool)
	SetVersion(version string)
}

// Loader 定义加载器接口
type Loader interface {
	Name() string
	Load(h Hoster) error
	Support(cfg *internal.Config, db *gorm.DB) bool
	Priority() int
}
