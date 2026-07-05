package protocol

import "strings"

// Relation 表示类之间的关系（约束为一等对象：一条外键约束对应一条关系）
// 单列关系存 SourceField/TargetField；复合外键存 SourceFields/TargetFields（按序对齐），
// 消费方统一经 SourceColumns/TargetColumns 读取
type Relation struct {
	Type         RelationType `json:"type"`                   // 关系类型
	Name         string       `json:"name,omitempty"`         // 约束名或推导键（关系集合去重的唯一标识）
	Through      *Through     `json:"through,omitempty"`      // 多对多配置
	SourceClass  string       `json:"sourceClass"`            // 源类名
	SourceField  string       `json:"sourceField"`            // 源字段名（单列；复合时为首列）
	SourceFields []string     `json:"sourceFields,omitempty"` // 复合外键源列组（len>1时生效）
	TargetClass  string       `json:"targetClass"`            // 目标类名
	TargetField  string       `json:"targetField"`            // 目标字段名（单列；复合时为首列）
	TargetFields []string     `json:"targetFields,omitempty"` // 复合外键目标列组（与源列组按序对齐）
	Deep         bool         `json:"deep,omitempty"`         // 深度递归（descendants/ancestors全树遍历）
}

// SourceColumns 源列组：复合外键返回列组，单列关系返回单元素切片
func (my *Relation) SourceColumns() []string {
	if len(my.SourceFields) > 1 {
		return my.SourceFields
	}
	return []string{my.SourceField}
}

// TargetColumns 目标列组，与SourceColumns按序对齐
func (my *Relation) TargetColumns() []string {
	if len(my.TargetFields) > 1 {
		return my.TargetFields
	}
	return []string{my.TargetField}
}

// Composite 是否复合外键关系（多列联合指向）
func (my *Relation) Composite() bool { return len(my.SourceFields) > 1 }

// Clone 复制关系并换型，reverse为true时交换源和目标方向（含Through键）
func (my *Relation) Clone(relType RelationType, reverse bool) *Relation {
	result := &Relation{
		Type:         relType,
		Name:         my.Name,
		SourceClass:  my.SourceClass,
		SourceField:  my.SourceField,
		SourceFields: my.SourceFields,
		TargetClass:  my.TargetClass,
		TargetField:  my.TargetField,
		TargetFields: my.TargetFields,
	}
	if reverse {
		result.SourceClass, result.TargetClass = result.TargetClass, result.SourceClass
		result.SourceField, result.TargetField = result.TargetField, result.SourceField
		result.SourceFields, result.TargetFields = result.TargetFields, result.SourceFields
	}
	if my.Through != nil {
		through := *my.Through
		if reverse {
			through.SourceKey, through.TargetKey = through.TargetKey, through.SourceKey
		}
		result.Through = &through
	}
	return result
}

// Key 关系身份键：源/目标的类与列组 + 类型 + 中间表，用于关系集合去重
// （不含Name：db约束名与config推导键描述同一关系时应视为同一条）
func (my *Relation) Key() string {
	key := string(my.Type) + "|" + my.SourceClass + "." + strings.Join(my.SourceColumns(), ",") +
		">" + my.TargetClass + "." + strings.Join(my.TargetColumns(), ",")
	if my.Through != nil {
		key += "|" + my.Through.TableName + "." + my.Through.SourceKey + "," + my.Through.TargetKey
	}
	if my.Deep {
		key += "|deep"
	}
	return key
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
