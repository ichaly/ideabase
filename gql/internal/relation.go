package internal

// Relation 表示类之间的关系
type Relation struct {
	SourceClass string       `json:"sourceClass"`       // 源类名
	SourceFiled string       `json:"sourceFiled"`       // 源字段名
	TargetTable string       `json:"targetTable"`       // 目标类名
	TargetFiled string       `json:"targetFiled"`       // 目标字段名
	Type        RelationType `json:"type"`              // 关系类型
	Through     *Through     `json:"through,omitempty"` // 多对多关系配置
}
