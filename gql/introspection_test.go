package gql

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ichaly/ideabase/utl"
	"github.com/stretchr/testify/assert"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// mockPgsqlDialect 测试用的PostgreSQL方言实现
type mockPgsqlDialect struct{}

func (m *mockPgsqlDialect) QuoteIdentifier() string {
	return "`"
}

// Placeholder 获取参数占位符 (PostgreSQL使用$1,$2...)
func (my *mockPgsqlDialect) Placeholder(index int) string {
	return fmt.Sprintf("$%d", index)
}

// FormatLimit 格式化LIMIT子句
func (my *mockPgsqlDialect) FormatLimit(limit, offset int) string {
	if limit <= 0 && offset <= 0 {
		return ""
	}

	var result string
	if limit > 0 {
		result = fmt.Sprintf("LIMIT %d", limit)
	}

	if offset > 0 {
		if len(result) > 0 {
			result += " "
		}
		result += fmt.Sprintf("OFFSET %d", offset)
	}

	return result
}

// BuildQuery 构建查询语句
func (my *mockPgsqlDialect) BuildQuery(ctx *Compiler, selectionSet ast.SelectionSet) error {
	ctx.Write("SELECT * FROM ")
	return nil
}

// BuildMutation 构建变更语句
func (my *mockPgsqlDialect) BuildMutation(ctx *Compiler, selectionSet ast.SelectionSet) error {
	ctx.Write("-- PostgreSQL mutation placeholder")
	return nil
}

// SupportsReturning 是否支持RETURNING子句
func (my *mockPgsqlDialect) SupportsReturning() bool {
	return true
}

// SupportsWithCTE 是否支持WITH CTE
func (my *mockPgsqlDialect) SupportsWithCTE() bool {
	return true
}

func TestGqlParserSchema(t *testing.T) {
	// 从cfg/schema.graphql文件中读取数据
	data, err := os.ReadFile(filepath.Join(utl.Root(), "cfg/schema.graphql"))
	assert.NoError(t, err)

	schema, err := gqlparser.LoadSchema(&ast.Source{
		Name:  "test.graphql",
		Input: string(data),
	})
	assert.NoError(t, err)

	t.Log(schema)
}

// TestIntrospection 测试自省功能
func TestIntrospection(t *testing.T) {
	// 注册测试用的PostgreSQL方言
	RegisterDialect("postgresql", &mockPgsqlDialect{})

	// 从数据库或模拟数据获取元数据
	meta, err := getTestMetadata(t)
	if err != nil {
		t.Skipf("跳过测试: %v", err)
	}
	// 创建渲染器
	renderer := NewRenderer(meta)

	// 创建测试执行器
	executor, err := NewExecutor(nil, renderer, meta, nil) // 直接使用元数据初始化执行器
	assert.NoError(t, err)

	// 测试__schema查询
	t.Run("Schema Introspection", func(t *testing.T) {
		query := `
		{
			__schema {
				queryType { name }
				types { name kind }
			}
		}
		`

		result := executor.Execute(context.Background(), query, nil, "")
		assert.Empty(t, result.Errors)
		assert.NotNil(t, result.Data)

		// 验证结果
		schemaData := result.Data["__schema"]
		assert.NotNil(t, schemaData, "结果中应包含__schema")

		schemaDataMap, ok := schemaData.(map[string]interface{})
		assert.True(t, ok, "schemaData应为map[string]interface{}")

		// 验证queryType
		queryType, ok := schemaDataMap["queryType"].(map[string]string)
		assert.True(t, ok, "结果中应包含queryType")
		assert.Equal(t, "Query", queryType["name"])

		// 验证types
		types, ok := schemaDataMap["types"].([]map[string]interface{})
		assert.True(t, ok, "结果中应包含types")
		assert.NotEmpty(t, types)
	})

	// 测试__type查询
	t.Run("Type Introspection", func(t *testing.T) {
		query := `
		{
			__type(name: "User") {
				name
				kind
				fields {
					name
					type {
						name
						kind
					}
				}
			}
		}
		`

		// 构造带有name参数的变量
		variables := map[string]interface{}{
			"name": "User",
		}

		result := executor.Execute(context.Background(), query, variables, "")
		assert.Empty(t, result.Errors)
		assert.NotNil(t, result.Data)

		// 验证结果
		typeData := result.Data["__type"]
		assert.NotNil(t, typeData, "结果中应包含__type")

		typeDataMap, ok := typeData.(map[string]interface{})
		assert.True(t, ok, "__type应为map[string]interface{}")

		assert.Equal(t, "User", typeDataMap["name"])
		assert.Equal(t, "OBJECT", fmt.Sprintf("%s", typeDataMap["kind"]))

		// 验证字段
		fields, ok := typeDataMap["fields"].([]map[string]interface{})
		assert.True(t, ok, "结果中应包含fields")
		assert.NotEmpty(t, fields)
	})
}
