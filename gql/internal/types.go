package internal

import (
	"encoding/json"
)

// ChainKind 表示关系链接类型
type ChainKind string

// 关系类型常量
const (
	MANY_TO_ONE  ChainKind = "many_to_one"  // 多对一关系
	ONE_TO_MANY  ChainKind = "one_to_many"  // 一对多关系
	MANY_TO_MANY ChainKind = "many_to_many" // 多对多关系
	RECURSIVE    ChainKind = "recursive"    // 递归关系
)

// Symbol 表示操作符号
type Symbol struct {
	Name  string
	Value string
	Desc  string
}

// Class 表示一个数据类/表的完整定义
type Class struct {
	Name        string            // 类名（可能是转换后的名称）
	Table       string            // 原始表名
	Virtual     bool              // 是否为虚拟类
	PrimaryKeys []string          // 主键列表
	Description string            // 描述信息
	Fields      map[string]*Field // 字段映射表(包含字段名和列名的索引)
}

// Field 表示类的一个字段
type Field struct {
	Type        string    `json:"type"`        // 类型
	Name        string    `json:"name"`        // 字段名
	Column      string    `json:"column"`      // 列名
	Virtual     bool      `json:"virtual"`     // 是否虚拟字段
	Nullable    bool      `json:"nullable"`    // 是否可空
	IsPrimary   bool      `json:"isPrimary"`   // 是否主键
	IsUnique    bool      `json:"isUnique"`    // 是否唯一
	Description string    `json:"description"` // 描述信息
	Relation    *Relation `json:"relation"`    // 若为关系字段,指向关系定义
}

// Relation 表示类之间的关系
type Relation struct {
	SourceClass string    `json:"sourceClass"` // 源类名
	SourceField string    `json:"sourceField"` // 源字段名
	TargetClass string    `json:"targetClass"` // 目标类名
	TargetField string    `json:"targetField"` // 目标字段名
	Kind        ChainKind `json:"kind"`        // 关系类型
	Reverse     *Relation `json:"-"`           // 反向关系引用
}

// AddField 添加字段到类中
func (my *Class) AddField(field *Field) {
	if my.Fields == nil {
		my.Fields = make(map[string]*Field)
	}

	// 添加字段名索引
	my.Fields[field.Name] = field

	// 如果列名与字段名不同，添加列名索引
	if field.Column != "" && field.Column != field.Name {
		my.Fields[field.Column] = field
	}
}

// GetField 获取字段定义(支持字段名或列名)
func (my *Class) GetField(nameOrColumn string) *Field {
	return my.Fields[nameOrColumn]
}

// RemoveField 移除字段
func (my *Class) RemoveField(field *Field) {
	if field == nil {
		return
	}
	// 删除字段名索引
	delete(my.Fields, field.Name)

	// 如果列名与字段名不同，删除列名索引
	if field.Column != "" && field.Column != field.Name {
		delete(my.Fields, field.Column)
	}
}

// MarshalJSON 实现自定义的JSON序列化
func (my *Class) MarshalJSON() ([]byte, error) {
	// 创建一个新的Fields映射，只包含主字段
	fields := make(map[string]*Field)
	for key, field := range my.Fields {
		// 只添加字段名等于Name的字段（主字段）
		if field.Name == key {
			fields[key] = field
		}
	}

	// 使用匿名结构体并直接初始化进行序列化
	return json.Marshal(struct {
		Name        string            `json:"name"`
		Table       string            `json:"table"`
		Virtual     bool              `json:"virtual"`
		PrimaryKeys []string          `json:"primaryKeys"`
		Description string            `json:"description"`
		Fields      map[string]*Field `json:"fields"`
	}{
		Name:        my.Name,
		Table:       my.Table,
		Fields:      fields,
		Virtual:     my.Virtual,
		PrimaryKeys: my.PrimaryKeys,
		Description: my.Description,
	})
}

// LoadOption 元数据加载选项
type LoadOption func() error
