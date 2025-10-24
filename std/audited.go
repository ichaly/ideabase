package std

import (
	"reflect"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

type Audited struct{}

func NewAudited() gorm.Plugin {
	return &Audited{}
}

func (my Audited) Name() string {
	return "gorm-audited"
}

func (my Audited) Initialize(db *gorm.DB) error {
	if err := db.Callback().Create().Before("gorm:create").Register(my.Name()+":before_create", my.beforeCreate); err != nil {
		return err
	}
	if err := db.Callback().Update().Before("gorm:update").Register(my.Name()+":before_update", my.beforeUpdate); err != nil {
		return err
	}
	if err := db.Callback().Delete().Before("gorm:delete").Register(my.Name()+":before_delete", my.beforeDelete); err != nil {
		return err
	}
	if err := db.Callback().Query().Before("gorm:query").Register(my.Name()+":before_query", my.beforeQuery); err != nil {
		return err
	}
	return nil
}

func (my Audited) beforeCreate(db *gorm.DB) {
	if db.Error != nil || db.Statement.Schema == nil {
		return
	}
	if db.Statement.Dest == nil {
		// 没有写入目标，直接返回避免空指针
		return
	}
	now := my.now(db)
	if db.Statement.Context != nil {
		if id := GetAuditUser(db.Statement.Context); id > 0 {
			my.setValue(db, "CreatedBy", &id)
			my.setValue(db, "UpdatedBy", &id)
		}
	}
	// 创建时保持 Created/Updated 成对写入，便于后续审计
	my.setValue(db, "CreatedAt", now)
	my.setValue(db, "UpdatedAt", now)
}

func (my Audited) beforeUpdate(db *gorm.DB) {
	if db.Error != nil || db.Statement.Schema == nil {
		return
	}
	now := my.now(db)
	if db.Statement.Context != nil {
		if id := GetAuditUser(db.Statement.Context); id > 0 {
			// 使用 SetColumn 保持 gorm 的字段切换与钩子行为
			db.Statement.SetColumn("UpdatedBy", &id, true)
		}
	}
	if db.Statement.Schema.LookUpField("UpdatedAt") != nil {
		// UpdatedAt 同样通过 Statement 写入，确保更新语句包含该列
		db.Statement.SetColumn("UpdatedAt", now, true)
	}
}

func (my Audited) beforeDelete(db *gorm.DB) {
	if db.Error != nil || db.Statement.Schema == nil {
		return
	}

	// 仅对包含软删字段的模型执行审计处理
	if my.hasSoftDeleteField(db.Statement.Schema) {
		now := my.now(db)
		deletedAtField := db.Statement.Schema.LookUpField("DeletedAt")
		if deletedAtField != nil {
			// 软删除时间依赖 GORM 自动构造的 update set
			db.Statement.SetColumn("DeletedAt", now, true)
		}

		if db.Statement.Context != nil {
			if id := GetAuditUser(db.Statement.Context); id > 0 {
				db.Statement.SetColumn("DeletedBy", &id, true)
			}
		}

		if !db.Statement.Unscoped && deletedAtField != nil {
			// 未开启 Unscoped 时才注入过滤条件
			SoftDeleteQueryClause{Field: deletedAtField}.ModifyStatement(db.Statement)
		}

		db.Statement.AddClauseIfNotExists(clause.Update{})
	}
}

func (my Audited) beforeQuery(db *gorm.DB) {
	if db.Error != nil || db.Statement.Schema == nil {
		return
	}

	if !db.Statement.Unscoped && my.hasSoftDeleteField(db.Statement.Schema) {
		if deletedAtField := db.Statement.Schema.LookUpField("DeletedAt"); deletedAtField != nil {
			// 复用软删 QueryClause，保持查询过滤一致
			SoftDeleteQueryClause{Field: deletedAtField}.ModifyStatement(db.Statement)
		}
	}
}

func (my Audited) setValue(db *gorm.DB, fieldName string, value interface{}) {
	stmt := db.Statement
	if stmt == nil || stmt.Schema == nil {
		return
	}
	// 借助 Schema 查找字段，保持与 GORM 的字段映射一致（含嵌套与命名类型）
	field := stmt.Schema.LookUpField(fieldName)
	if field == nil {
		// 模型未定义该字段，忽略即可
		return
	}
	my.assignSchemaValue(stmt, field, stmt.ReflectValue, value)
}

func (my Audited) assignSchemaValue(stmt *gorm.Statement, field *schema.Field, target reflect.Value, value interface{}) {
	if !target.IsValid() {
		return
	}
	switch target.Kind() {
	case reflect.Ptr:
		if target.IsNil() {
			if !target.CanSet() {
				// 只读指针无法赋值，直接跳过
				return
			}
			target.Set(reflect.New(target.Type().Elem()))
		}
		my.assignSchemaValue(stmt, field, target.Elem(), value)
	case reflect.Struct:
		// 统一使用 Field.Set，利用 GORM 内置的类型转换与命名类型支持
		if err := field.Set(stmt.Context, target, value); err != nil {
			_ = stmt.AddError(err)
		}
	case reflect.Slice, reflect.Array:
		// slices/arrays 逐个赋值以兼容批量 Insert/Update
		for i := 0; i < target.Len(); i++ {
			my.assignSchemaValue(stmt, field, target.Index(i), value)
		}
	default:
		// 其他类型不会出现（例如 map），因此无需特殊处理
	}
}

func (my Audited) hasSoftDeleteField(s *schema.Schema) bool {
	// 是否存在 DeletedAt 字段，用于判定软删能力
	return s.LookUpField("DeletedAt") != nil
}

func (my Audited) now(db *gorm.DB) time.Time {
	switch {
	case db != nil && db.Statement != nil && db.Statement.DB != nil && db.Statement.DB.NowFunc != nil:
		// 优先使用当前 statement 上下文配置的 NowFunc（事务/Session 级别）
		return db.Statement.DB.NowFunc()
	case db != nil && db.NowFunc != nil:
		return db.NowFunc()
	default:
		// 回退到标准库时间，避免因为 NowFunc 被覆盖导致空指针
		return time.Now()
	}
}

// SoftDeleteQueryClause 软删除查询子句
type SoftDeleteQueryClause struct {
	Field *schema.Field
}

// 这里保留 GORM clause.Interface 的完整方法签名，原因：
// 1. 与官方 soft_delete 实现保持同构，方便阅读/对照；
// 2. 日后若改为注册到 GORM 的 QueryClauses 流程，可直接复用；
// 3. 当前仅在 ModifyStatement 中生效，其余方法按接口要求留空。
func (my SoftDeleteQueryClause) Name() string {
	return ""
}

func (my SoftDeleteQueryClause) Build(clause.Builder) {
}

func (my SoftDeleteQueryClause) MergeClause(*clause.Clause) {
}

func (my SoftDeleteQueryClause) ModifyStatement(stmt *gorm.Statement) {
	if stmt == nil {
		// Statement 缺失时无法注入过滤条件
		return
	}
	unscoped := stmt.Statement != nil && stmt.Statement.Unscoped
	if _, ok := stmt.Clauses["soft_delete_enabled"]; ok || unscoped {
		// 已处理或显式 Unscoped，保持调用方期望
		return
	}
	// 同步 GORM 原生软删除行为：单个 OR 条件需要转 AND 才能继续追加约束
	if c, ok := stmt.Clauses["WHERE"]; ok {
		if where, ok := c.Expression.(clause.Where); ok && len(where.Exprs) >= 1 {
			for _, expr := range where.Exprs {
				if orCond, ok := expr.(clause.OrConditions); ok && len(orCond.Exprs) == 1 {
					where.Exprs = []clause.Expression{clause.And(where.Exprs...)}
					c.Expression = where
					stmt.Clauses["WHERE"] = c
					break
				}
			}
		}
	}
	column := "deleted_at"
	if my.Field != nil && my.Field.DBName != "" {
		// Schema 提供的列名优先，以支持自定义 tag
		column = my.Field.DBName
	}
	// 使用实际列名，兼容自定义 gorm tag
	stmt.AddClause(clause.Where{Exprs: []clause.Expression{
		clause.Eq{Column: clause.Column{Table: clause.CurrentTable, Name: column}, Value: nil},
	}})
	// 打标避免重复注入
	stmt.Clauses["soft_delete_enabled"] = clause.Clause{}
}
