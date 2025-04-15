package compiler

import (
	"testing"
)

func TestMySQLFormat(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "简单SELECT语句",
			input:    `SELECT                  id, name FROM users WHERE age > 18`,
			expected: "select id, name from users where age > 18",
		},
		{
			name:     "带JOIN的SELECT语句",
			input:    "SELECT u.id, u.name, p.title FROM users u JOIN posts p ON u.id = p.user_id WHERE u.age > 18",
			expected: "select u.id, u.name, p.title from users as u join posts as p on u.id = p.user_id where u.age > 18",
		},
		{
			name:     "带ORDER BY和LIMIT的SELECT语句",
			input:    "SELECT id, name FROM users ORDER BY created_at DESC LIMIT 10",
			expected: "select id, name from users order by created_at desc limit 10",
		},
		{
			name:     "带换行的SQL语句",
			input:    "SELECT id, name\nFROM users\nWHERE age > 18",
			expected: "select id, name from users where age > 18",
		},
		{
			name:     "带制表符的SQL语句",
			input:    "SELECT\tid,\tname\tFROM\tusers\tWHERE\tage > 18",
			expected: "select id, name from users where age > 18",
		},
		{
			name:     "复杂子查询",
			input:    "SELECT u.id, u.name FROM users u WHERE u.id IN (SELECT user_id FROM orders WHERE amount > 100)",
			expected: "select u.id, u.name from users as u where u.id in (select user_id from orders where amount > 100)",
		},
		{
			name:     "带别名的表达式",
			input:    "SELECT COUNT(*) AS total, SUM(amount) AS sum_amount FROM orders",
			expected: "select COUNT(*) as total, SUM(amount) as sum_amount from orders",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Format(tt.input, "mysql")
			if result != tt.expected {
				t.Errorf("Format() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestMySQLNormalize(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "简单SELECT语句",
			input:    "SELECT id, name FROM users WHERE age > 18",
			expected: "select id, name from users where age > ?",
		},
		{
			name:     "带字符串的SELECT语句",
			input:    "SELECT * FROM users WHERE name = 'John'",
			expected: "select * from users where name = ?",
		},
		{
			name:     "带多个参数的INSERT语句",
			input:    "INSERT INTO users (name, email, age) VALUES ('John', 'john@example.com', 25)",
			expected: "insert into users(name, email, age) values (?, ?, ?)",
		},
		{
			name:     "带特殊字符的字符串",
			input:    "SELECT * FROM users WHERE name = 'O''Reilly'",
			expected: "select * from users where name = ?",
		},
		{
			name:     "带多个条件的WHERE子句",
			input:    "SELECT * FROM users WHERE age > 18 AND status = 'active' OR role = 'admin'",
			expected: "select * from users where age > ? and `status` = ? or role = ?",
		},
		{
			name:     "带IN操作符的查询",
			input:    "SELECT * FROM users WHERE id IN (1, 2, 3, 4)",
			expected: "select * from users where id in (?, ?, ?, ?)",
		},
		{
			name:     "带BETWEEN操作符的查询",
			input:    "SELECT * FROM users WHERE age BETWEEN 18 AND 30",
			expected: "select * from users where age between ? and ?",
		},
		{
			name:     "带日期的查询",
			input:    "SELECT * FROM users WHERE created_at > '2023-01-01'",
			expected: "select * from users where created_at > ?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Normalize(tt.input, "mysql")
			if result != tt.expected {
				t.Errorf("Normalize() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestPostgreSQLFormat(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "简单SELECT语句",
			input:    "SELECT id, name FROM users WHERE age > 18",
			expected: "select id, name from users where age > 18",
		},
		{
			name:     "带JOIN的SELECT语句",
			input:    "SELECT u.id, u.name, p.title FROM users u JOIN posts p ON u.id = p.user_id WHERE u.age > 18",
			expected: "select u.id, u.name, p.title from users as u join posts as p on u.id = p.user_id where u.age > 18",
		},
		{
			name:     "带ORDER BY和LIMIT的SELECT语句",
			input:    "SELECT id, name FROM users ORDER BY created_at DESC LIMIT 10",
			expected: "select id, name from users order by created_at desc limit 10",
		},
		{
			name:     "带换行和缩进的SQL语句",
			input:    "SELECT\n  id,\n  name\nFROM\n  users\nWHERE\n  age > 18",
			expected: "select id, name from users where age > 18",
		},

		{
			name:     "带GROUP BY和HAVING的查询",
			input:    "SELECT department, COUNT(*) FROM employees GROUP BY department HAVING COUNT(*) > 10",
			expected: "select department, COUNT(*) from employees group by department having COUNT(*) > 10",
		},
		{
			name:     "带联合查询的SQL",
			input:    "SELECT id, name FROM users UNION SELECT id, title FROM posts",
			expected: "select id, name from users union select id, title from posts",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Format(tt.input, "postgres")
			if result != tt.expected {
				t.Errorf("Format() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestPostgreSQLNormalize(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "简单SELECT语句",
			input:    "SELECT id, name FROM users WHERE age > 18",
			expected: "select id, name from users where age > $1",
		},
		{
			name:     "带字符串的SELECT语句",
			input:    "SELECT * FROM users WHERE name = 'John'",
			expected: "select * from users where name = $1",
		},
		{
			name:     "带多个参数的INSERT语句",
			input:    "INSERT INTO users (name, email, age) VALUES ('John', 'john@example.com', 25)",
			expected: "insert into users(name, email, age) values ($1, $2, $3)",
		},
		{
			name:     "带参数的UPDATE语句",
			input:    "UPDATE users SET name = 'John', email = 'john@example.com' WHERE id = 1",
			expected: "update users set name = $1, email = $2 where id = $3",
		},
		{
			name:     "带特殊字符的字符串",
			input:    "SELECT * FROM users WHERE name = 'O''Reilly'",
			expected: "select * from users where name = $1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Normalize(tt.input, "postgres")
			if result != tt.expected {
				t.Errorf("Normalize() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestInvalidSQL(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		dbType string
	}{
		{
			name:   "缺少列名的SELECT",
			input:  "SELECT FROM users",
			dbType: "mysql",
		},

		{
			name:   "语法错误的WHERE子句",
			input:  "SELECT * FROM users WHERE",
			dbType: "mysql",
		},
		{
			name:   "不匹配的引号",
			input:  "SELECT * FROM users WHERE name = 'John",
			dbType: "mysql",
		},
		{
			name:   "非法的SQL关键字",
			input:  "SLCT * FROM users",
			dbType: "mysql",
		},
		{
			name:   "PostgreSQL的无效SQL",
			input:  "SELECT FROM users",
			dbType: "postgres",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" Format", func(t *testing.T) {
			result := Format(tt.input, tt.dbType)
			if result != "" {
				t.Errorf("Format() with invalid SQL should return empty string, got %v", result)
			}
		})

		t.Run(tt.name+" Normalize", func(t *testing.T) {
			result := Normalize(tt.input, tt.dbType)
			if result != "" {
				t.Errorf("Normalize() with invalid SQL should return empty string, got %v", result)
			}
		})
	}
}
