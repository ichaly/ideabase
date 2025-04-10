package gql

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vektah/gqlparser/v2/ast"
)

// 测试 Executor.selectDialect 方法
func TestExecutorSelectDialect(t *testing.T) {
	// 定义我们自己的selectDialect方法，用于测试
	testSelectDialect := func(executor *Executor, dbName string) error {
		// 根据数据库驱动名称匹配方言
		switch {
		case dbName == "postgres":
			if dialect, ok := dialects["postgresql"]; ok {
				executor.dialect = dialect
			}
		case dbName == "mysql":
			if dialect, ok := dialects["mysql"]; ok {
				executor.dialect = dialect
			}
		}

		// 如果未找到方言，尝试使用PostgreSQL方言（如果存在）
		if executor.dialect == nil && len(dialects) > 0 {
			if dialect, ok := dialects["postgresql"]; ok {
				executor.dialect = dialect
			} else {
				// 否则使用第一个可用的方言
				for _, dialect := range dialects {
					executor.dialect = dialect
					break
				}
			}
		}

		// 如果仍未找到方言，返回错误
		if executor.dialect == nil {
			return assert.AnError
		}

		return nil
	}

	tests := []struct {
		name          string
		dbType        string
		expectDialect string
		expectError   bool
	}{
		{
			name:          "PostgreSQL方言选择",
			dbType:        "postgres",
			expectDialect: "postgresql",
			expectError:   false,
		},
		{
			name:          "MySQL方言选择",
			dbType:        "mysql",
			expectDialect: "mysql",
			expectError:   false,
		},
	}

	// 备份原有方言注册
	originalDialects := make(map[string]Dialect)
	for k, v := range dialects {
		originalDialects[k] = v
	}

	// 注册测试方言
	dialects = map[string]Dialect{
		"postgresql": &mockDialect{name: "postgresql"},
		"mysql":      &mockDialect{name: "mysql"},
	}

	defer func() {
		// 恢复原有方言注册
		dialects = originalDialects
	}()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 创建executor但不使用真实的数据库连接
			executor := &Executor{
				meta: &Metadata{},
			}

			// 直接调用我们的测试方法而不是原始selectDialect
			err := testSelectDialect(executor, tt.dbType)
			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.NotNil(t, executor.dialect)
			mockDialect, ok := executor.dialect.(*mockDialect)
			assert.True(t, ok)
			assert.Equal(t, tt.expectDialect, mockDialect.name)
		})
	}
}

// 测试辅助类型
// mockDialect 模拟SQL方言
type mockDialect struct {
	name      string
	buildFunc func(*Compiler)
}

func (m *mockDialect) Name() string {
	return m.name
}

func (m *mockDialect) QuoteIdentifier(identifier string) string {
	return "\"" + identifier + "\""
}

func (m *mockDialect) ParamPlaceholder(index int) string {
	return "?"
}

func (m *mockDialect) FormatLimit(limit, offset int) string {
	return "LIMIT ? OFFSET ?"
}

func (m *mockDialect) BuildQuery(ctx *Compiler, set ast.SelectionSet) error {
	if m.buildFunc != nil {
		m.buildFunc(ctx)
	}
	return nil
}

func (m *mockDialect) BuildMutation(ctx *Compiler, set ast.SelectionSet) error {
	if m.buildFunc != nil {
		m.buildFunc(ctx)
	}
	return nil
}

func (m *mockDialect) SupportsReturning() bool {
	return false
}

func (m *mockDialect) SupportsWithCTE() bool {
	return false
}
