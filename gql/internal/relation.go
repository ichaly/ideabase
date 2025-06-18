package internal

// Relation 表示类之间的关系
type Relation struct {
	SourceTable  string       `json:"sourceTable"`       // 源类名
	SourceColumn string       `json:"sourceColumn"`      // 源字段名
	TargetTable  string       `json:"targetTable"`       // 目标类名
	TargetColumn string       `json:"targetColumn"`      // 目标字段名
	MiddleTable  string       `json:"middleTable"`       // 目标字段名
	MiddleColumn []string     `json:"middleColumn"`      // 目标字段名
	Type         RelationType `json:"type"`              // 关系类型
	Reverse      *Relation    `json:"-"`                 // 反向关系引用
	Through      *Through     `json:"through,omitempty"` // 多对多关系配置
}
