package protocol

// Relation 表示类之间的关系
type Relation struct {
	Type        RelationType `json:"type"`              // 关系类型
	Through     *Through     `json:"through,omitempty"` // 多对多配置
	SourceClass string       `json:"sourceClass"`       // 源类名
	SourceFiled string       `json:"sourceFiled"`       // 源字段名
	TargetClass string       `json:"targetClass"`       // 目标类名
	TargetFiled string       `json:"targetFiled"`       // 目标字段名
	Deep        bool         `json:"deep,omitempty"`    // 深度递归（descendants/ancestors全树遍历）
}

// Through 表示多对多关系中的中间表配置
type Through struct {
	TableName string `json:"tableName"` // 中间表名称
	TargetKey string `json:"targetKey"` // 中间表中指向目标表的外键
	SourceKey string `json:"sourceKey"` // 中间表中指向源表的外键
}

// RelationType 表示关系类型
type RelationType string

// 关系类型常量
const (
	RECURSIVE    RelationType = "Recursive"  // 递归关系
	MANY_TO_ONE  RelationType = "ManyToOne"  // 多对一关系
	ONE_TO_MANY  RelationType = "OneToMany"  // 一对多关系
	MANY_TO_MANY RelationType = "ManyToMany" // 多对多关系
)

// RemoteRef 远程关系引用：字段值来自注册的远程数据源（Remote Join）
type RemoteRef struct {
	Source string `json:"source"` // 数据源名，执行期按名分发到Remote实现
	Key    string `json:"key"`    // 宿主键字段名，编译期自动补投影、执行期批量取数
}

// Alias 宿主键在补投影时使用的内部别名：避开客户端选择集与codec路径，
// 保证执行期拿到的是未经转换的原始键值，回填后剥除
func (my *RemoteRef) Alias() string { return "__rk_" + my.Key }
