package gql

import (
	"context"
	"fmt"
	"testing"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/stretchr/testify/assert"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// mockPgsqlDialect 测试用的PostgreSQL方言实现
type mockPgsqlDialect struct{}

func (m *mockPgsqlDialect) Quote() string {
	return `"`
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
func (my *mockPgsqlDialect) BuildQuery(ctx *compiler.Context, selectionSet ast.SelectionSet) error {
	ctx.Write("SELECT * FROM ")
	return nil
}

// BuildMutation 构建变更语句
func (my *mockPgsqlDialect) BuildMutation(ctx *compiler.Context, selectionSet ast.SelectionSet) error {
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
	// 由mock元数据现场生成schema，避免依赖预生成文件
	data, err := NewRenderer(createMockMetadata(t)).Generate()
	assert.NoError(t, err)

	schema, err := gqlparser.LoadSchema(&ast.Source{
		Name:  "test.graphql",
		Input: data,
	})
	assert.NoError(t, err)

	t.Log(schema)
}

// TestIntrospection 测试自省功能：标准IntrospectionQuery（GraphiQL/codegen同款）直接对接
func TestIntrospection(t *testing.T) {
	meta, err := getTestMetadata(t)
	if err != nil {
		t.Skipf("跳过测试: %v", err)
	}
	executor, err := NewExecutor(nil, NewRenderer(meta), meta, nil)
	assert.NoError(t, err)
	ctx := context.Background()

	t.Run("标准IntrospectionQuery", func(t *testing.T) {
		result := executor.Execute(ctx, standardIntrospectionQuery, nil, "IntrospectionQuery")
		assert.Empty(t, result.Errors, "标准自省查询失败: %v", result.Errors)

		schema := result.Data["__schema"].(map[string]interface{})
		assert.Equal(t, "Query", schema["queryType"].(map[string]interface{})["name"])
		assert.Equal(t, "Mutation", schema["mutationType"].(map[string]interface{})["name"])
		assert.Equal(t, "Subscription", schema["subscriptionType"].(map[string]interface{})["name"])

		// 类型清单完整：实体、输入、内省元类型都在
		names := map[string]map[string]interface{}{}
		for _, item := range schema["types"].([]interface{}) {
			node := item.(map[string]interface{})
			names[node["name"].(string)] = node
		}
		for _, expected := range []string{"User", "UserResult", "UserWhereInput", "UserCreateInput", "__Schema", "__Type"} {
			assert.Contains(t, names, expected)
		}

		// ofType链：User.posts类型 [Post]! -> NON_NULL -> LIST -> Post
		var posts map[string]interface{}
		for _, f := range names["User"]["fields"].([]interface{}) {
			if field := f.(map[string]interface{}); field["name"] == "posts" {
				posts = field
			}
		}
		assert.NotNil(t, posts, "User应有posts字段")
		nonNull := posts["type"].(map[string]interface{})
		assert.Equal(t, "NON_NULL", nonNull["kind"])
		listRef := nonNull["ofType"].(map[string]interface{})
		assert.Equal(t, "LIST", listRef["kind"])
		assert.Equal(t, "Post", listRef["ofType"].(map[string]interface{})["name"])

		// 输入类型有inputFields且含NON_NULL链
		inputs := names["UserCreateInput"]["inputFields"].([]interface{})
		assert.NotEmpty(t, inputs)
		var nameInput map[string]interface{}
		for _, item := range inputs {
			if node := item.(map[string]interface{}); node["name"] == "name" {
				nameInput = node
			}
		}
		assert.NotNil(t, nameInput)
		assert.Equal(t, "NON_NULL", nameInput["type"].(map[string]interface{})["kind"])

		// 指令完整
		directives := map[string]bool{}
		for _, item := range schema["directives"].([]interface{}) {
			directives[item.(map[string]interface{})["name"].(string)] = true
		}
		assert.True(t, directives["include"] && directives["skip"])
	})

	t.Run("Type Introspection", func(t *testing.T) {
		result := executor.Execute(ctx, `{ __type(name: "User") { name kind fields { name type { name kind } } } }`, nil, "")
		assert.Empty(t, result.Errors)

		typeData := result.Data["__type"].(map[string]interface{})
		assert.Equal(t, "User", typeData["name"])
		assert.Equal(t, "OBJECT", typeData["kind"])
		assert.NotEmpty(t, typeData["fields"])
	})

	t.Run("变量传name", func(t *testing.T) {
		result := executor.Execute(ctx, `query ($n: String!) { __type(name: $n) { name } }`,
			map[string]interface{}{"n": "Post"}, "")
		assert.Empty(t, result.Errors)
		assert.Equal(t, "Post", result.Data["__type"].(map[string]interface{})["name"])
	})

	t.Run("未知类型返回null", func(t *testing.T) {
		result := executor.Execute(ctx, `{ __type(name: "Nope") { name } }`, nil, "")
		assert.Empty(t, result.Errors)
		assert.Nil(t, result.Data["__type"])
	})
}

// standardIntrospectionQuery GraphiQL/graphql-js getIntrospectionQuery() 的标准文本
const standardIntrospectionQuery = `
query IntrospectionQuery {
  __schema {
    queryType { name }
    mutationType { name }
    subscriptionType { name }
    types { ...FullType }
    directives { name description locations args { ...InputValue } }
  }
}
fragment FullType on __Type {
  kind name description
  fields(includeDeprecated: true) {
    name description
    args { ...InputValue }
    type { ...TypeRef }
    isDeprecated deprecationReason
  }
  inputFields { ...InputValue }
  interfaces { ...TypeRef }
  enumValues(includeDeprecated: true) { name description isDeprecated deprecationReason }
  possibleTypes { ...TypeRef }
}
fragment InputValue on __InputValue {
  name description
  type { ...TypeRef }
  defaultValue
}
fragment TypeRef on __Type {
  kind name
  ofType { kind name ofType { kind name ofType { kind name ofType { kind name ofType { kind name ofType { kind name ofType { kind name } } } } } } }
}
`
