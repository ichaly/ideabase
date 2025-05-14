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
type Hoster interface {
	// PutClass 添加或者合并一个类节点
	PutClass(class *internal.Class) error
	// GetClass 获取一个类节点
	GetClass(className string) (*internal.Class, bool)
	// DelClass 删除一个类节点
	DelClass(className string)
	// PutField 为类添加或者合并一个字段
	PutField(className string, field *internal.Field) error
	// GetField 获取一个类的字段
	GetField(className, fieldName string) (*internal.Field, bool)
	// DelField 删除一个类的字段
	DelField(className, fieldName string)
	// 设置版本号
	SetVersion(version string)
}

// Loader 定义加载器接口
type Loader interface {
	Name() string
	Load(h Hoster) error
	Support(cfg *internal.Config, db *gorm.DB) bool
	Priority() int
}
