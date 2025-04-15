// SQL格式化和归一化工具
// 使用github.com/xwb1989/sqlparser实现SQL格式化和归一化

package compiler

import (
	"fmt"
	"strings"

	"github.com/xwb1989/sqlparser"
)

// Format 格式化SQL语句
// 使用sqlparser将SQL语句格式化为标准格式
// dbType参数用于兼容旧版接口，实际上不再区分数据库类型
// 如果解析失败，返回空字符串
func Format(sql string, dbType string) string {
	// 解析SQL语句
	stmt, err := sqlparser.Parse(sql)
	if err != nil {
		return ""
	}

	// 格式化SQL语句
	return sqlparser.String(stmt)
}

// Normalize 归一化SQL语句
// 将SQL语句中的字面量替换为占位符
// 对于MySQL，使用?作为占位符
// 对于PostgreSQL，使用$1, $2等作为占位符
func Normalize(sql string, dbType string) string {
	// 解析SQL语句
	stmt, err := sqlparser.Parse(sql)
	if err != nil {
		return ""
	}

	// 根据数据库类型选择不同的占位符格式
	var formatFunc func(*sqlparser.TrackedBuffer, sqlparser.SQLNode)
	if strings.ToLower(dbType) == "postgres" {
		// PostgreSQL使用$1, $2等作为占位符
		placeholderCount := 0
		formatFunc = func(buf *sqlparser.TrackedBuffer, node sqlparser.SQLNode) {
			switch node.(type) {
			case *sqlparser.SQLVal:
				placeholderCount++
				buf.WriteString(fmt.Sprintf("$%d", placeholderCount))
			default:
				node.Format(buf)
			}
		}
	} else {
		// MySQL使用?作为占位符
		formatFunc = func(buf *sqlparser.TrackedBuffer, node sqlparser.SQLNode) {
			switch node.(type) {
			case *sqlparser.SQLVal:
				buf.WriteString("?")
			default:
				node.Format(buf)
			}
		}
	}

	// 使用自定义格式化函数归一化SQL
	buf := sqlparser.NewTrackedBuffer(formatFunc)
	stmt.Format(buf)
	return buf.String()
}
