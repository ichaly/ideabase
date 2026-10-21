package sql

import (
	"sort"
	"strings"
)

// Tags 只接受单条 SELECT；显式写入、锁查询或解析失败时绕过缓存。
// 这里只提取 SQL 显式关系，视图和函数内部依赖仍需调用方补充。
func Tags(dialect, sql string) ([]string, bool) {
	switch strings.ToLower(dialect) {
	case "postgres", "postgresql":
		return extractPostgresTags(sql)
	case "mysql":
		return extractMySQLTags(sql)
	default:
		return nil, false
	}
}

// WriteTags 提取单条受支持 DML 的显式关系，用于写入后的表级失效。
// 保守地包含源表和 CTE 名称，可能多失效；不解析触发器和函数内部的依赖。
func WriteTags(dialect, query string) ([]string, bool) {
	switch strings.ToLower(dialect) {
	case "postgres", "postgresql":
		return extractPostgresWriteTags(query)
	case "mysql":
		return extractMySQLWriteTags(query)
	default:
		return nil, false
	}
}

func addTableTags(seen map[string]struct{}, schema, table string) {
	if table == "" {
		return
	}
	if schema != "" {
		seen[schema+"."+table] = struct{}{}
	}
	// 写入端可能只有未限定表名，保留叶子 tag，宁可跨 schema 多失效。
	seen[table] = struct{}{}
}

func sortedTags(seen map[string]struct{}) []string {
	result := make([]string, 0, len(seen))
	for tag := range seen {
		result = append(result, tag)
	}
	sort.Strings(result)
	return result
}
