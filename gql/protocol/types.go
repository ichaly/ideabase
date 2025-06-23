package protocol

type Node interface {
	Name() string
}

// Tree 定义元数据承载者接口
type Tree interface {
	// PutNode 添加或者合并一个类节点
	PutNode(name string, node *Class) error
	// GetNode 获取一个类节点
	GetNode(name string) (*Class, bool)
	// SetVersion 设置版本号
	SetVersion(version string)
}

// Loader 定义加载器接口
type Loader interface {
	Name() string
	Load(t Tree) error
	Support() bool
	Priority() int
}
