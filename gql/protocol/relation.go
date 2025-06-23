package protocol

// Relation 表示类之间的关系
type Relation struct {
	Type        RelationType `json:"type"`              // 关系类型
	Through     *Through     `json:"through,omitempty"` // 多对多配置
	SourceClass string       `json:"sourceClass"`       // 源类名
	SourceFiled string       `json:"sourceFiled"`       // 源字段名
	TargetClass string       `json:"targetClass"`       // 目标类名
	TargetFiled string       `json:"targetFiled"`       // 目标字段名
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
	RECURSIVE    RelationType = "recursive"    // 递归关系
	MANY_TO_ONE  RelationType = "many_to_one"  // 多对一关系
	ONE_TO_MANY  RelationType = "one_to_many"  // 一对多关系
	MANY_TO_MANY RelationType = "many_to_many" // 多对多关系
)

// Parse 从字符串转换为关系类型
func (my RelationType) Parse(kind string) RelationType {
	switch kind {
	case string(ONE_TO_MANY):
		return ONE_TO_MANY
	case string(MANY_TO_MANY):
		return MANY_TO_MANY
	case string(RECURSIVE):
		return RECURSIVE
	default:
		return MANY_TO_ONE // 默认为多对一
	}
}
