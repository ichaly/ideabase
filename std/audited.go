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

	dest := db.Statement.Dest
	if dest == nil {
		return
	}

	if db.Statement.Context != nil {
		if user, ok := GetUserFromContext(db.Statement.Context); ok {
			my.setFieldForEntity(dest, "CreatedBy", user)
		}
	}

	if my.hasField(dest, "CreatedAt") {
		my.setFieldForEntity(dest, "CreatedAt", time.Now())
	}
}

func (my Audited) beforeUpdate(db *gorm.DB) {
	if db.Error != nil || db.Statement.Schema == nil {
		return
	}

	if db.Statement.Context != nil {
		if user, ok := GetUserFromContext(db.Statement.Context); ok {
			db.Statement.SetColumn("UpdatedBy", user, true)
		}
	}

	if db.Statement.Schema.LookUpField("UpdatedAt") != nil {
		db.Statement.SetColumn("UpdatedAt", time.Now(), true)
	}
}

func (my Audited) beforeDelete(db *gorm.DB) {
	if db.Error != nil || db.Statement.Schema == nil {
		return
	}

	if my.hasSoftDeleteField(db.Statement.Schema) {
		now := time.Now()

		deletedAtField := db.Statement.Schema.LookUpField("DeletedAt")
		if deletedAtField != nil {
			db.Statement.SetColumn("DeletedAt", now, true)
		}

		if db.Statement.Context != nil {
			if user, ok := GetUserFromContext(db.Statement.Context); ok {
				db.Statement.SetColumn("DeletedBy", user, true)
			}
		}

		if !db.Statement.Unscoped {
			SoftDeleteQueryClause{Field: deletedAtField}.ModifyStatement(db.Statement)
		}

		db.Statement.AddClauseIfNotExists(clause.Update{})
	}
}

func (my Audited) beforeQuery(db *gorm.DB) {
	if db.Error != nil || db.Statement.Schema == nil {
		return
	}

	// 如果没有设置 Unscoped，且模型有软删除字段，则自动添加过滤条件
	if !db.Statement.Unscoped && my.hasSoftDeleteField(db.Statement.Schema) {
		deletedAtField := db.Statement.Schema.LookUpField("DeletedAt")
		if deletedAtField != nil {
			// 检查是否已经设置了 soft_delete_enabled 子句
			if _, ok := db.Statement.Clauses["soft_delete_enabled"]; !ok {
				// 处理现有的 WHERE 子句
				if c, ok := db.Statement.Clauses["WHERE"]; ok {
					if where, ok := c.Expression.(clause.Where); ok && len(where.Exprs) >= 1 {
						// 检查是否有单个 OR 条件需要转换为 AND
						for _, expr := range where.Exprs {
							if orCond, ok := expr.(clause.OrConditions); ok && len(orCond.Exprs) == 1 {
								where.Exprs = []clause.Expression{clause.And(where.Exprs...)}
								c.Expression = where
								db.Statement.Clauses["WHERE"] = c
								break
							}
						}
					}
				}

				// 添加软删除过滤条件
				db.Statement.AddClause(clause.Where{Exprs: []clause.Expression{
					clause.Eq{Column: clause.Column{Table: clause.CurrentTable, Name: "deleted_at"}, Value: nil},
				}})
				db.Statement.Clauses["soft_delete_enabled"] = clause.Clause{}
			}
		}
	}
}

func (my Audited) setFieldForEntity(dest interface{}, fieldName string, value interface{}) {
	destValue := reflect.ValueOf(dest)
	if destValue.Kind() == reflect.Ptr {
		destValue = destValue.Elem()
	}

	if destValue.Kind() != reflect.Struct {
		return
	}

	// 遍历所有字段，包括嵌入的结构体
	for i := 0; i < destValue.NumField(); i++ {
		field := destValue.Type().Field(i)
		fieldValue := destValue.Field(i)

		// 处理嵌入字段
		if field.Anonymous {
			if fieldValue.Kind() == reflect.Ptr {
				if fieldValue.IsNil() {
					fieldValue.Set(reflect.New(fieldValue.Type().Elem()))
				}
				fieldValue = fieldValue.Elem()
			}
			if fieldValue.Kind() == reflect.Struct {
				my.setFieldForEntity(fieldValue.Addr().Interface(), fieldName, value)
				continue
			}
		}

		// 检查字段名是否匹配
		if field.Name == fieldName && fieldValue.IsValid() && fieldValue.CanSet() {
			if fieldValue.Kind() == reflect.Ptr {
				if fieldValue.IsNil() {
					fieldValue.Set(reflect.New(fieldValue.Type().Elem()))
				}
				targetValue := reflect.ValueOf(value)
				if targetValue.Kind() == reflect.Ptr {
					fieldValue.Elem().Set(targetValue.Elem())
				} else {
					fieldValue.Elem().Set(targetValue)
				}
			} else {
				targetValue := reflect.ValueOf(value)
				if targetValue.Kind() == reflect.Ptr {
					fieldValue.Set(targetValue.Elem())
				} else {
					fieldValue.Set(targetValue)
				}
			}
		}
	}
}

func (my Audited) hasField(dest interface{}, fieldName string) bool {
	destValue := reflect.ValueOf(dest)
	if destValue.Kind() == reflect.Ptr {
		destValue = destValue.Elem()
	}

	if destValue.Kind() != reflect.Struct {
		return false
	}

	for i := 0; i < destValue.NumField(); i++ {
		field := destValue.Type().Field(i)
		if field.Anonymous {
			if my.hasField(destValue.Field(i).Addr().Interface(), fieldName) {
				return true
			}
		}
		if field.Name == fieldName {
			return true
		}
	}
	return false
}

func (my Audited) hasSoftDeleteField(s *schema.Schema) bool {
	return s.LookUpField("DeletedAt") != nil
}

// SoftDeleteQueryClause 软删除查询子句
type SoftDeleteQueryClause struct {
	Field *schema.Field
}

func (my SoftDeleteQueryClause) Name() string {
	return ""
}

func (my SoftDeleteQueryClause) Build(clause.Builder) {
}

func (my SoftDeleteQueryClause) MergeClause(*clause.Clause) {
}

func (my SoftDeleteQueryClause) ModifyStatement(stmt *gorm.Statement) {
	if _, ok := stmt.Clauses["soft_delete_enabled"]; !ok && !stmt.Statement.Unscoped {
		stmt.AddClause(clause.Where{Exprs: []clause.Expression{
			clause.Eq{Column: clause.Column{Table: clause.CurrentTable, Name: "deleted_at"}, Value: nil},
		}})
		stmt.Clauses["soft_delete_enabled"] = clause.Clause{}
	}
}
