package internal

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
