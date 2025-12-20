package std

import "gorm.io/gorm"

// Migratable 用于为单个实体提供“无侵入”的迁移扩展能力（如：联合索引、函数索引、JSON/GIN 索引、方言差异 SQL 等）。
//
// 约定：
// - 实现必须幂等（可重复执行），建议使用 IF EXISTS/IF NOT EXISTS 或 Migrator 检查；
// - dialect 使用 gorm 的 db.Dialector.Name()（如 "postgres" / "mysql"）。
type Migratable interface {
	Migrate(db *gorm.DB, dialect string) error
}

func migrateEntity(db *gorm.DB, entity any) error {
	m, ok := entity.(Migratable)
	if !ok {
		return nil
	}
	return m.Migrate(db, db.Dialector.Name())
}
