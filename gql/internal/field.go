package internal

// Field 表示类的一个字段/列的完整定义
type Field struct {
	Type        string    `json:"type"`        // 类型
	Name        string    `json:"name"`        // 字段名
	Column      string    `json:"column"`      // 列名
	Virtual     bool      `json:"virtual"`     // 是否虚拟字段
	Original    bool      `json:"original"`    // 是否原始字段
	Nullable    bool      `json:"nullable"`    // 是否可空
	IsUnique    bool      `json:"isUnique"`    // 是否唯一
	IsPrimary   bool      `json:"isPrimary"`   // 是否主键
	IsThrough   bool      `json:"isThrough"`   // 是否为中间表关系字段
	IsList      bool      `json:"isList"`      // 是否是集合类型
	Description string    `json:"description"` // 描述信息
	Relation    *Relation `json:"relation"`    // 若为关系字段,指向关系定义
	Resolver    string    `json:"resolver"`    // 字段级别自定义Resolver
}
