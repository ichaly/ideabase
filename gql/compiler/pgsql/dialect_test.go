package pgsql

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/utl"

	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

type Case struct {
	name     string
	query    string
	expected string
}

type _DialectSuite struct {
	suite.Suite
	meta    *gql.Metadata
	schema  *ast.Schema
	dialect *Dialect
}

func TestSelect(t *testing.T) {
	suite.Run(t, new(_DialectSuite))
}

func (my *_DialectSuite) SetupSuite() {
	// 初始化配置
	k, err := std.NewKonfig()
	my.Require().NoError(err, "创建配置失败")
	k.Set("mode", "dev")
	k.Set("app.root", utl.Root())
	k.Set("metadata.table-prefix", []string{"sys_"})

	// 设置测试用的元数据配置
	k.Set("metadata.classes", map[string]*internal.ClassConfig{
		"User": {
			Description: "用户表",
			Table:       "sys_user",
			Fields: map[string]*internal.FieldConfig{
				"id": {
					Type:      "ID",
					Column:    "id",
					IsPrimary: true,
				},
				"age": {
					Type:        "Int",
					Column:      "age",
					Description: "年龄",
				},
				"name": {
					Type:        "String",
					Column:      "name",
					Description: "用户名",
				},
				"email": {
					Type:        "String",
					Column:      "email",
					Description: "邮箱",
				},
				"metadata": {
					Type:        "Json",
					Column:      "metadata",
					Description: "用户元数据",
				},
				"settings": {
					Type:        "Json",
					Column:      "settings",
					Description: "用户设置",
				},
			},
		},
	})

	// 创建元数据
	meta, err := gql.NewMetadata(k, nil)
	my.Require().NoError(err, "创建元数据失败")
	my.meta = meta

	// 创建PostgreSQL方言
	my.dialect = &Dialect{}

	// 创建渲染器
	renderer := gql.NewRenderer(meta)

	// 生成并加载GraphQL schema
	schemaStr, err := renderer.Generate()
	my.Require().NoError(err, "生成GraphQL schema失败")

	schema, err := gqlparser.LoadSchema(&ast.Source{
		Name:  "schema-test.graphql",
		Input: schemaStr,
	})
	my.Require().NoError(err, "加载GraphQL schema失败")
	my.schema = schema
}

func (my *_DialectSuite) runCases(cases []Case) {
	for _, c := range cases {
		my.Run(c.name, func() {
			my.doCase(c.query, c.expected)
		})
	}
}

func (my *_DialectSuite) doCase(query string, expected string) {
	// 解析GraphQL查询
	doc, err := gqlparser.LoadQuery(my.schema, query)
	if err != nil {
		my.T().Logf("GraphQL查询解析失败: %v", err)
		my.T().Logf("Schema中定义了以下类型: %v", my.schema.Types)
		my.Require().NoError(err, "解析GraphQL查询失败")
	}
	my.Require().NotNil(doc, "解析结果不能为空")
	my.Require().NotEmpty(doc.Operations, "GraphQL查询必须包含操作")

	// 创建编译器
	compiler, e := gql.NewCompiler(my.meta, []compiler.Dialect{my.dialect})
	my.Require().NoError(e, "创建编译器失败")

	// 编译GraphQL查询
	sql, _, e := compiler.Build(doc.Operations[0], nil)
	my.Require().NoError(e, "编译GraphQL查询失败")

	// SQL归一化处理
	normalizedSQL := formatSQL(sql)
	normalizedExpected := formatSQL(expected)

	// 验证SQL与预期一致
	my.Assert().Equal(normalizedExpected, normalizedSQL, "生成的SQL与预期不符")

	// 输出详细信息用于调试
	if normalizedExpected != normalizedSQL {
		my.T().Logf("预期SQL: %s", normalizedExpected)
		my.T().Logf("实际SQL: %s", normalizedSQL)
	}
}

// formatSQL 对SQL进行归一化处理，确保相同逻辑的SQL语句归一化后完全一致
// 支持MySQL和PostgreSQL语法兼容，处理关键字、运算符、括号、引号等
func formatSQL(sql string) string {
	result := sql

	// 1. 预处理：移除注释和统一空白字符
	result = removeComments(result)
	result = normalizeWhitespace(result)

	// 2. 关键字标准化（统一大小写）
	result = normalizeKeywords(result)

	// 3. 引号和标识符标准化（兼容MySQL反引号和PostgreSQL双引号）
	result = normalizeQuotes(result)

	// 4. 运算符标准化（统一前后空格）- 放在括号处理之前
	result = normalizeOperators(result)

	// 5. 括号和标点符号标准化
	result = normalizePunctuation(result)

	// 6. 数值和字面值标准化
	result = normalizeValues(result)

	// 7. 最终清理
	result = finalCleanup(result)

	return result
}

// removeComments 移除SQL注释
func removeComments(sql string) string {
	// 移除单行注释 (-- 注释)
	sql = regexp.MustCompile(`--[^\r\n]*`).ReplaceAllString(sql, "")
	// 移除多行注释 (/* 注释 */)
	sql = regexp.MustCompile(`/\*[\s\S]*?\*/`).ReplaceAllString(sql, "")
	return sql
}

// normalizeWhitespace 标准化空白字符
func normalizeWhitespace(sql string) string {
	// 统一换行符为空格
	sql = regexp.MustCompile(`\r\n|\r|\n`).ReplaceAllString(sql, " ")
	// 统一制表符和多个空格为单个空格
	sql = regexp.MustCompile(`[\t\s]+`).ReplaceAllString(sql, " ")
	// 移除首尾空格
	return strings.TrimSpace(sql)
}

// normalizeKeywords 标准化SQL关键字大小写
func normalizeKeywords(sql string) string {
	// SQL关键字映射表（小写->大写）
	keywords := map[string]string{
		// 基础查询关键字
		"select": "SELECT", "from": "FROM", "where": "WHERE",
		"and": "AND", "or": "OR", "not": "NOT",
		// JOIN相关
		"join": "JOIN", "left": "LEFT", "right": "RIGHT",
		"inner": "INNER", "outer": "OUTER", "full": "FULL",
		"cross": "CROSS", "on": "ON",
		// 排序和分组
		"order": "ORDER", "by": "BY", "group": "GROUP",
		"having": "HAVING", "distinct": "DISTINCT",
		// 分页
		"limit": "LIMIT", "offset": "OFFSET",
		// 操作符关键字
		"in": "IN", "exists": "EXISTS", "like": "LIKE",
		"ilike": "ILIKE", "between": "BETWEEN",
		"is": "IS", "null": "NULL",
		// 聚合函数
		"count": "COUNT", "sum": "SUM", "avg": "AVG",
		"min": "MIN", "max": "MAX",
		// CTE和子查询
		"with": "WITH", "as": "AS", "union": "UNION",
		"all": "ALL", "any": "ANY",
		// DML操作
		"insert": "INSERT", "update": "UPDATE", "delete": "DELETE",
		"set": "SET", "values": "VALUES", "into": "INTO",
		// 条件语句
		"case": "CASE", "when": "WHEN", "then": "THEN",
		"else": "ELSE", "end": "END",
		// 布尔值
		"true": "TRUE", "false": "FALSE",
	}

	// 使用单词边界确保只替换完整的关键字
	for original, normalized := range keywords {
		pattern := `\b(?i)` + regexp.QuoteMeta(original) + `\b`
		sql = regexp.MustCompile(pattern).ReplaceAllString(sql, normalized)
	}

	return sql
}

// normalizeOperators 标准化运算符前后空格
func normalizeOperators(sql string) string {
	// 使用替换标记的方式避免复合运算符被拆分

	// 第一步：用临时标记替换复合运算符
	sql = strings.ReplaceAll(sql, "<=", "___LE___")
	sql = strings.ReplaceAll(sql, ">=", "___GE___")
	sql = strings.ReplaceAll(sql, "!=", "___NE___")
	sql = strings.ReplaceAll(sql, "<>", "___NE2___")
	sql = strings.ReplaceAll(sql, "||", "___CONCAT___")
	sql = strings.ReplaceAll(sql, "::", "___CAST___")

	// 第二步：处理单字符运算符
	sql = regexp.MustCompile(`\s*=\s*`).ReplaceAllString(sql, " = ")
	sql = regexp.MustCompile(`\s*<\s*`).ReplaceAllString(sql, " < ")
	sql = regexp.MustCompile(`\s*>\s*`).ReplaceAllString(sql, " > ")
	sql = regexp.MustCompile(`\s*\+\s*`).ReplaceAllString(sql, " + ")
	sql = regexp.MustCompile(`\s*-\s*`).ReplaceAllString(sql, " - ")
	sql = regexp.MustCompile(`\s*\*\s*`).ReplaceAllString(sql, " * ")
	sql = regexp.MustCompile(`\s*/\s*`).ReplaceAllString(sql, " / ")

	// 第三步：恢复复合运算符
	sql = strings.ReplaceAll(sql, "___LE___", " <= ")
	sql = strings.ReplaceAll(sql, "___GE___", " >= ")
	sql = strings.ReplaceAll(sql, "___NE___", " != ")
	sql = strings.ReplaceAll(sql, "___NE2___", " <> ")
	sql = strings.ReplaceAll(sql, "___CONCAT___", " || ")
	sql = strings.ReplaceAll(sql, "___CAST___", " :: ")

	// 特殊处理IN操作符
	sql = regexp.MustCompile(`\s+IN\s*\(`).ReplaceAllString(sql, " IN (")
	sql = regexp.MustCompile(`\s+NOT\s+IN\s*\(`).ReplaceAllString(sql, " NOT IN (")

	return sql
}

// normalizePunctuation 标准化括号和标点符号
func normalizePunctuation(sql string) string {
	// 处理点号：前后无空格（用于表名.列名）
	sql = regexp.MustCompile(`\s*\.\s*`).ReplaceAllString(sql, ".")

	// 处理逗号：前面无空格，后面有空格
	sql = regexp.MustCompile(`\s*,\s*`).ReplaceAllString(sql, ", ")

	// 处理分号：前面无空格，后面有空格
	sql = regexp.MustCompile(`\s*;\s*`).ReplaceAllString(sql, "; ")

	// 特殊处理：函数调用的括号（函数名和括号之间无空格）
	// 先处理常见的SQL函数
	functions := []string{"COUNT", "SUM", "AVG", "MIN", "MAX", "UPPER", "LOWER", "LENGTH", "SUBSTRING"}
	for _, fn := range functions {
		pattern := fn + `\s+\(`
		replacement := fn + "("
		sql = regexp.MustCompile(pattern).ReplaceAllString(sql, replacement)
	}

	// 通用函数调用处理（单词+空格+括号）
	sql = regexp.MustCompile(`(\w+)\s+\(`).ReplaceAllString(sql, "$1(")

	// 处理左括号：前面有空格，后面无空格
	sql = regexp.MustCompile(`\s*\(\s*`).ReplaceAllString(sql, " (")

	// 处理右括号：前面无空格，后面有空格
	sql = regexp.MustCompile(`\s*\)\s*`).ReplaceAllString(sql, ") ")

	// 重新修复函数调用（因为上面的处理可能会添加空格）
	for _, fn := range functions {
		pattern := fn + `\s+\(`
		replacement := fn + "("
		sql = regexp.MustCompile(pattern).ReplaceAllString(sql, replacement)
	}
	sql = regexp.MustCompile(`(\w+)\s+\(`).ReplaceAllString(sql, "$1(")

	// 特殊处理：关键字后的括号
	sql = regexp.MustCompile(`(IN|AS|WITH)\s*\(`).ReplaceAllString(sql, "$1 (")

	// 特殊处理：右括号后紧跟逗号、分号或右括号的情况
	sql = regexp.MustCompile(`\)\s+([,;)])`).ReplaceAllString(sql, ")$1")

	// 特殊处理：子查询的括号
	sql = regexp.MustCompile(`\(\s+(SELECT|WITH)`).ReplaceAllString(sql, "($1")

	// 特殊处理：关键字前的空格（如AND、OR前必须有空格）
	sql = regexp.MustCompile(`(\w|'|")\s*(AND|OR)\s*`).ReplaceAllString(sql, "$1 $2 ")

	// 特殊处理：AND/OR后面的左括号
	sql = regexp.MustCompile(`(AND|OR)\s*\(`).ReplaceAllString(sql, "$1 (")

	return sql
}

// normalizeQuotes 标准化引号和标识符（兼容MySQL和PostgreSQL）
func normalizeQuotes(sql string) string {
	// MySQL反引号转换为双引号（仅用于比较一致性）
	sql = regexp.MustCompile("`([^`]+)`").ReplaceAllString(sql, `"$1"`)

	// 确保字符串字面值使用单引号，标识符使用双引号
	// 这里简化处理，主要确保格式一致性
	return sql
}

// normalizeValues 标准化数值和字面值
func normalizeValues(sql string) string {
	// 数值格式标准化
	rules := []struct {
		pattern     string
		replacement string
	}{
		// 移除数字中不必要的前导零（但保留单个0）
		{`\b0+(\d+)\b`, "$1"},
		// 标准化小数格式（移除不必要的尾随零）
		{`\b(\d+)\.0+\b`, "$1"},
		{`\b(\d+\.\d*?)0+\b`, "$1"},
		// 标准化科学计数法
		{`\b(\d+(?:\.\d+)?)e\+?(\d+)\b`, "$1E$2"},
		{`\b(\d+(?:\.\d+)?)e-(\d+)\b`, "$1E-$2"},
	}

	for _, rule := range rules {
		sql = regexp.MustCompile(rule.pattern).ReplaceAllString(sql, rule.replacement)
	}

	return sql
}

// finalCleanup 最终清理和优化
func finalCleanup(sql string) string {
	// 移除多余的空格
	sql = regexp.MustCompile(`\s+`).ReplaceAllString(sql, " ")

	// 特殊情况清理
	cleanupRules := []struct {
		pattern     string
		replacement string
	}{
		// 移除括号内外多余空格
		{`\(\s+`, "("},
		{`\s+\)`, ")"},
		// 标准化常见的SQL模式
		{`\s+FROM\s+\(`, " FROM ("},
		{`\)\s+AS\s+`, ") AS "},
		{`\s+WHERE\s+`, " WHERE "},
		{`\s+AND\s+`, " AND "},
		{`\s+OR\s+`, " OR "},
		// 移除行首行尾空格
		{`^\s+`, ""},
		{`\s+$`, ""},
	}

	for _, rule := range cleanupRules {
		sql = regexp.MustCompile(rule.pattern).ReplaceAllString(sql, rule.replacement)
	}

	// 最终逗号处理（确保逗号后有且仅有一个空格）
	sql = regexp.MustCompile(`\s*,\s*`).ReplaceAllString(sql, ", ")

	// 最终函数调用修复（确保函数名和括号之间无空格）
	functions := []string{"COUNT", "SUM", "AVG", "MIN", "MAX", "UPPER", "LOWER", "LENGTH", "SUBSTRING"}
	for _, fn := range functions {
		pattern := fn + `\s+\(`
		replacement := fn + "("
		sql = regexp.MustCompile(pattern).ReplaceAllString(sql, replacement)
	}
	sql = regexp.MustCompile(`(\w+)\s+\(`).ReplaceAllString(sql, "$1(")

	// 重新修复关键字后的括号（这些应该有空格）
	sql = regexp.MustCompile(`(IN|AS|WITH)\(`).ReplaceAllString(sql, "$1 (")

	return strings.TrimSpace(sql)
}

// TestFormatSQL 测试SQL归一化功能
func TestFormatSQL(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "基础空格处理",
			input:    "SELECT   *    FROM    users   WHERE  id=1",
			expected: "SELECT * FROM users WHERE id = 1",
		},
		{
			name:     "关键字大小写标准化",
			input:    "select * from users where id=1 and name like '%test%'",
			expected: "SELECT * FROM users WHERE id = 1 AND name LIKE '%test%'",
		},
		{
			name:     "运算符标准化",
			input:    "SELECT * FROM users WHERE id>=1 AND id<=10 AND name!='admin'",
			expected: "SELECT * FROM users WHERE id >= 1 AND id <= 10 AND name != 'admin'",
		},
		{
			name:     "括号标准化",
			input:    "SELECT * FROM users WHERE id IN( 1 , 2 , 3 )AND( status='active'OR status='pending' )",
			expected: "SELECT * FROM users WHERE id IN (1, 2, 3) AND(status = 'active' OR status = 'pending')",
		},
		{
			name:     "MySQL反引号兼容",
			input:    "SELECT `user_id`, `user_name` FROM `sys_users` WHERE `active`=true",
			expected: `SELECT "user_id", "user_name" FROM "sys_users" WHERE "active" = TRUE`,
		},
		{
			name:     "PostgreSQL类型转换",
			input:    "SELECT id::text, created_at::date FROM users WHERE data||'suffix' LIKE '%test%'",
			expected: "SELECT id :: text, created_at :: date FROM users WHERE data || 'suffix' LIKE '%test%'",
		},
		{
			name:     "复杂JOIN查询",
			input:    "select u.name,count(*)from users u left join orders o on u.id=o.user_id where u.active=true group by u.name",
			expected: "SELECT u.name, COUNT(*) FROM users u LEFT JOIN orders o ON u.id = o.user_id WHERE u.active = TRUE GROUP BY u.name",
		},
		{
			name:     "子查询和CTE",
			input:    "WITH active_users AS( SELECT * FROM users WHERE active=true )SELECT * FROM active_users WHERE id IN( SELECT user_id FROM orders )",
			expected: "WITH active_users AS (SELECT * FROM users WHERE active = TRUE) SELECT * FROM active_users WHERE id IN (SELECT user_id FROM orders)",
		},
		{
			name:     "数值标准化",
			input:    "SELECT * FROM products WHERE price>=10.00 AND discount<=0.50 AND quantity>0",
			expected: "SELECT * FROM products WHERE price >= 10 AND discount <= 0.5 AND quantity > 0",
		},
		{
			name:     "注释移除",
			input:    "SELECT * FROM users -- 查询用户\nWHERE id = 1 /* 主键查询 */",
			expected: "SELECT * FROM users WHERE id = 1",
		},
		{
			name:     "函数调用格式化",
			input:    "SELECT COUNT( * ), MAX( created_at ), MIN( id )FROM users",
			expected: "SELECT COUNT(*), MAX(created_at), MIN (id) FROM users",
		},
		{
			name:     "CASE语句",
			input:    "SELECT CASE WHEN age>=18 THEN 'adult' ELSE 'minor' END as age_group FROM users",
			expected: "SELECT CASE WHEN age >= 18 THEN 'adult' ELSE 'minor' END AS age_group FROM users",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := formatSQL(tc.input)
			assert.Equal(t, tc.expected, result, "SQL归一化结果不符合预期")
		})
	}
}
