package protocol

import "github.com/ichaly/ideabase/utl"

// Class 表示一个数据类/表的完整定义
type Class struct {
	Name        string            `json:"name"`               // 类名（可能是转换后的名称）
	Table       string            `json:"table"`              // 原始表名
	Virtual     bool              `json:"virtual"`            // 是否为虚拟类
	Original    bool              `json:"original"`           // 是否为原始类
	PrimaryKeys []string          `json:"primaryKeys"`        // 主键列表
	Description string            `json:"description"`        // 描述信息
	Fields      map[string]*Field `json:"fields"`             // 字段映射表(包含字段名和列名的索引)
	Resolver    string            `json:"resolver,omitempty"` // 类级别自定义Resolver
	IsThrough   bool              `json:"isThrough"`          // 是否为中间表关系表
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

// DelField 移除字段
func (my *Class) DelField(field *Field) {
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
	return utl.Marshal(Class{
		Name:        my.Name,
		Table:       my.Table,
		Fields:      fields,
		Virtual:     my.Virtual,
		PrimaryKeys: my.PrimaryKeys,
		Description: my.Description,
		Resolver:    my.Resolver,
	})
}
