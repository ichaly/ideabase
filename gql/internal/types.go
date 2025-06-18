package internal

// RelationType 表示关系类型
type RelationType string

// 关系类型常量
const (
	MANY_TO_ONE  RelationType = "many_to_one"  // 多对一关系
	ONE_TO_MANY  RelationType = "one_to_many"  // 一对多关系
	MANY_TO_MANY RelationType = "many_to_many" // 多对多关系
	RECURSIVE    RelationType = "recursive"    // 递归关系
)

// Symbol 表示操作符号
type Symbol struct {
	Name        string
	Value       string
	Description string
}

// FromString 从字符串转换为关系类型
func (my RelationType) FromString(kind string) RelationType {
	switch kind {
	case string(ONE_TO_MANY):
		return ONE_TO_MANY
	case string(MANY_TO_ONE):
		return MANY_TO_ONE
	case string(MANY_TO_MANY):
		return MANY_TO_MANY
	case string(RECURSIVE):
		return RECURSIVE
	default:
		return MANY_TO_ONE // 默认为多对一
	}
}

// Reverse 获取反向关系类型
func (my RelationType) Reverse() RelationType {
	switch my {
	case ONE_TO_MANY:
		return MANY_TO_ONE
	case MANY_TO_ONE:
		return ONE_TO_MANY
	case MANY_TO_MANY:
		return MANY_TO_MANY
	case RECURSIVE:
		return RECURSIVE
	default:
		return ONE_TO_MANY // 默认为一对多
	}
}
