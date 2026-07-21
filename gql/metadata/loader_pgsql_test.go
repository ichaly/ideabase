package metadata

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPgsqlMetaSQLCorrectness 对pgsql元数据SQL做静态断言（真实执行由gql包的testcontainers测试覆盖）：
//  1. regclass转换必须用%I标识符引用，否则大写/特殊字符表名整库加载失败；
//  2. 主外键必须走pg_constraint按conkey/confkey位置配对，
//     information_schema.constraint_column_usage无位置序号，复合外键会笛卡尔积错配，
//     且kcu/ccu按约束名join不过滤schema会跨schema串表；
//  3. 聚合必须带ORDER BY，否则外键遍历顺序随DB返回变化，叠加Relation单指针覆盖导致schema跨启动不稳定
func TestPgsqlMetaSQLCorrectness(t *testing.T) {
	sql := pgsqlMetaSQL

	// 1. 标识符引用
	assert.Contains(t, sql, "format('%I.%I', c.table_schema, c.table_name)::regclass",
		"regclass转换应使用%I按标识符引用")
	assert.NotContains(t, sql, "format('%s.%s'", "不应再使用%s拼接regclass")

	// 2. pg_constraint按位置配对
	assert.Contains(t, sql, "FROM pg_constraint con", "主外键应查pg_constraint")
	assert.Contains(t, sql, "unnest(con.conkey, con.confkey) WITH ORDINALITY",
		"复合外键应按conkey/confkey位置unnest配对")
	assert.Contains(t, sql, "con.contype = 'p'", "主键约束过滤")
	assert.Contains(t, sql, "con.contype = 'f'", "外键约束过滤")
	assert.Contains(t, sql, "nsp.nspname = $1", "约束应限定schema")
	assert.NotContains(t, sql, "constraint_column_usage",
		"不应再使用无位置序号的constraint_column_usage")

	// 3. 稳定排序：四段聚合各自带ORDER BY
	assert.Equal(t, 4, strings.Count(sql, "ORDER BY"), "tables/columns/primaryKeys/foreignKeys四段聚合都应排序")
	assert.Contains(t, sql, "ORDER BY fk.source_table, fk.constraint_name, fk.ord",
		"外键按表名、约束名、位置序号排序")
	assert.Contains(t, sql, "ORDER BY pk.table_name, pk.constraint_name, pk.ord",
		"主键按表名、约束名、位置序号排序")
	assert.Contains(t, sql, "ORDER BY c.table_name, c.ordinal_position", "列按表名、列序号排序")
}
