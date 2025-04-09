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
	// 从数据库或模拟数据获取元数据
	meta, err := getTestMetadata(t)
	if err != nil {
		t.Skipf("跳过测试: %v", err)
	}
	// 创建渲染器
	renderer := NewRenderer(meta)

	// 注册一个简单的测试方言实现
	RegisterTestDialect()
	
	// 创建测试执行器
	compiler, err := NewCompiler(nil) // 这里不需要元数据
	assert.NoError(t, err)
	executor, err := NewExecutor(nil, renderer, compiler)
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

		result := executor.Execute(context.Background(), query, nil)
		assert.Empty(t, result.Errors)
		assert.NotNil(t, result.Data)

		// 验证结果
		schemaData, ok := result.Data["__schema"].(map[string]interface{})
		assert.True(t, ok, "结果中应包含__schema")

		// 验证queryType
		queryType, ok := schemaData["queryType"].(map[string]string)
		assert.True(t, ok, "结果中应包含queryType")
		assert.Equal(t, "Query", queryType["name"])

		// 验证types
		types, ok := schemaData["types"].([]map[string]interface{})
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
		vars, err := json.Marshal(map[string]interface{}{
			"name": "User",
		})
		assert.NoError(t, err)

		result := executor.Execute(context.Background(), query, vars)
		assert.Empty(t, result.Errors)
		assert.NotNil(t, result.Data)

		// 验证结果
		typeData, ok := result.Data["__type"].(map[string]interface{})
		assert.True(t, ok, "结果中应包含__type")
		assert.Equal(t, "User", typeData["name"])
		assert.Equal(t, "OBJECT", fmt.Sprintf("%s", typeData["kind"]))

		// 验证字段
		fields, ok := typeData["fields"].([]map[string]interface{})
		assert.True(t, ok, "结果中应包含fields")
		assert.NotEmpty(t, fields)
	})
}

// RegisterTestDialect 注册一个简单的测试用SQL方言实现
func RegisterTestDialect() {
	// 如果尚未注册任何方言，则注册一个简单的测试方言
	if len(dialects) == 0 {
		// 注册一个最小化的测试方言
		RegisterDialect("test", &testDialect{})
	}
}

// testDialect 是一个简单的测试用SQL方言实现
type testDialect struct{}

func (d *testDialect) Name() string { return "test" }

func (d *testDialect) CompileQuery(meta *Metadata, query *ast.QueryDocument, vars map[string]interface{}) (*CompileResult, error) {
	// 返回一个最小化的编译结果，仅用于测试
	return &CompileResult{
		SQL:    "SELECT 1",
		Args:   []interface{}{},
		Fields: []*ResultField{},
	}, nil
}

func (d *testDialect) CompileSubscription(meta *Metadata, query *ast.QueryDocument, vars map[string]interface{}) (*CompileResult, error) {
	return d.CompileQuery(meta, query, vars)
}

func (d *testDialect) CompileMutation(meta *Metadata, query *ast.QueryDocument, vars map[string]interface{}) (*CompileResult, error) {
	return d.CompileQuery(meta, query, vars)
}
