package protocol

import "github.com/ichaly/ideabase/utl"

// Class 表示一个数据类/表的完整定义
type Class struct {
	Name        string            `json:"name"`                  // 类名（可能是转换后的名称）
	Table       string            `json:"table"`                 // 原始表名
	Virtual     bool              `json:"virtual"`               // 是否为虚拟类
	PrimaryKeys []string          `json:"primaryKeys"`           // 主键列表
	Description string            `json:"description"`           // 描述信息
	Fields      map[string]*Field `json:"fields"`                // 字段映射表(包含字段名和列名的索引)
	Relations   []*Relation       `json:"relations,omitempty"`   // 本类参与的关系集合（约束为一等对象，虚拟关系字段由此派生）
	Search      []string          `json:"search,omitempty"`      // 参与全文搜索的字段
	Scope       []ScopeRule       `json:"scope,omitempty"`       // 行级作用域：编译期强制注入的过滤（租户/属主隔离）
	IDGenerator string            `json:"idGenerator,omitempty"` // 主键策略：database/snowflake/自定义名
	Generate    IDGenerator       `json:"-"`                     // 启动时绑定，执行计划直接引用
	IsThrough   bool              `json:"isThrough"`             // 是否为中间表关系表
}

// AddRelation 追加关系到集合，按身份键去重（db与config声明同一关系时只保留一条）
func (my *Class) AddRelation(rel *Relation) {
	key := rel.Key()
	for _, exist := range my.Relations {
		if exist.Key() == key {
			return
		}
	}
	my.Relations = append(my.Relations, rel)
}

// ScopeRule 行级作用域规则：列 = 执行期从请求上下文取的值（认证注入，不进schema）
type ScopeRule struct {
	Column  string `json:"column"`  // 数据库列名（如 tenant_id / user_id）
	Context string `json:"context"` // 上下文键名（如 tenant / userId），值由 gql.WithScope 注入
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

// MarshalJSON 实现自定义的JSON序列化
func (my *Class) MarshalJSON() ([]byte, error) {
	// 只序列化主字段（字段名==key），且跳过虚拟关系字段——
	// 虚拟字段由Relations在构建期确定性再生，入文件会在重载时重复生成幽灵字段
	fields := make(map[string]*Field)
	for key, field := range my.Fields {
		if field.Name == key && !field.Virtual {
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
		Relations:   my.Relations, // 关系集合随元数据序列化，file路径由此再生虚拟字段
		Search:      my.Search,
		Scope:       my.Scope, // 作用域随元数据序列化，文件缓存路径不丢隔离配置
		IDGenerator: my.IDGenerator,
	})
}
