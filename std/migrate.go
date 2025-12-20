package std

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// migrate.go 负责“无侵入迁移扩展”：
// 1) 不改实体字段/tag：实体只需实现接口即可挂载迁移逻辑；
// 2) 与 gorm 升级解耦：仅依赖 gorm 公共 API（AutoMigrate + Dialector + Exec）；
// 3) 统一方言分支：将差异 SQL 生成集中在一处，避免散落在业务代码里。
//
// 触发时机在 NewDatabase：AutoMigrate 成功后依次调用 migrateComment / migrateEntity。

// Describer 为实体提供“表描述/注释”能力。
// 仅要求实现 Description()，不绑定任何 gorm tag/字段，便于跨项目复用与后续升级。
type Describer interface {
	Description() string
}

// Migratable 用于为单个实体提供“无侵入”的迁移扩展能力（如：联合索引、函数索引、JSON/GIN 索引、方言差异 SQL 等）。
//
// 约定：
// - 实现必须幂等（可重复执行），建议使用 IF EXISTS/IF NOT EXISTS 或 Migrator 检查；
// - dialect 使用 gorm 的 db.Dialector.Name()（如 "postgres" / "mysql"）。
type Migratable interface {
	Migrate(db *gorm.DB, dialect string) error
}

// migrateComment 为实现了 Describer 的实体写入“表注释”。
//
// 为什么不再用 gorm:table_options：
// - 各方言对 DDL/多语句支持不一致；把注释当成独立 SQL 更可控；
// - 便于做 sqlite 的兼容（sqlite 无原生表注释，改为写入元表）。
func migrateComment(db *gorm.DB, entity any) error {
	d, ok := entity.(Describer)
	if !ok {
		return nil
	}
	desc := strings.TrimSpace(d.Description())
	if desc == "" {
		return nil
	}
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(entity); err != nil || stmt.Schema == nil || stmt.Schema.Table == "" {
		return nil
	}
	return execAll(db, tableCommentSQL(db.Dialector.Name(), stmt.Schema.Table, desc))
}

// migrateEntity 调用实体自带的迁移扩展（联合索引、函数索引、GIN/JSON 索引等）。
func migrateEntity(db *gorm.DB, entity any) error {
	m, ok := entity.(Migratable)
	if !ok {
		return nil
	}
	return m.Migrate(db, db.Dialector.Name())
}

// execAll 顺序执行 SQL 切片，空 SQL 会被跳过。
func execAll(db *gorm.DB, sqls []string) error {
	for _, s := range sqls {
		if strings.TrimSpace(s) == "" {
			continue
		}
		if err := db.Exec(s).Error; err != nil {
			return err
		}
	}
	return nil
}

// tableCommentSQL 统一生成“表注释”SQL（按方言）。
//
// - postgres：原生 COMMENT ON TABLE
// - mysql：ALTER TABLE ... COMMENT
// - sqlite：写入 __ideabase_table_comment 元表，作为“注释能力”的兼容实现
func tableCommentSQL(dialect, table, comment string) []string {
	qTable := quoteIdent(dialect, table)
	qComment := quoteString(comment)

	switch dialect {
	case "postgres":
		return []string{fmt.Sprintf("COMMENT ON TABLE %s IS %s;", qTable, qComment)}
	case "mysql":
		return []string{fmt.Sprintf("ALTER TABLE %s COMMENT = %s;", qTable, qComment)}
	case "sqlite":
		return []string{
			`CREATE TABLE IF NOT EXISTS __ideabase_table_comment (` +
				`table_name TEXT PRIMARY KEY, ` +
				`comment TEXT NOT NULL, ` +
				`updated_at TEXT NOT NULL DEFAULT (datetime('now'))` +
				`);`,
			fmt.Sprintf(
				"INSERT INTO __ideabase_table_comment(table_name, comment) VALUES(%s, %s) "+
					"ON CONFLICT(table_name) DO UPDATE SET comment=excluded.comment, updated_at=datetime('now');",
				quoteString(table),
				qComment,
			),
		}
	default:
		return nil
	}
}

// quoteIdent 对标识符加方言引号（支持 schema.table）。
func quoteIdent(dialect, ident string) string {
	parts := strings.Split(strings.TrimSpace(ident), ".")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if dialect == "mysql" {
			parts[i] = "`" + strings.ReplaceAll(p, "`", "``") + "`"
			continue
		}
		parts[i] = `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
	}
	return strings.Join(parts, ".")
}

// quoteString 对 SQL 字符串做最小转义（单引号翻倍）。
func quoteString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
