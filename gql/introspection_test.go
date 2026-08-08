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
		result := executor.run(ctx, standardIntrospectionQuery, nil, "IntrospectionQuery")
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
		assert.False(t, directives["defer"], "未实现的@defer不得出现在自省契约")
	})

	t.Run("Type Introspection", func(t *testing.T) {
		result := executor.run(ctx, `{ __type(name: "User") { name kind fields { name type { name kind } } } }`, nil, "")
		assert.Empty(t, result.Errors)

		typeData := result.Data["__type"].(map[string]interface{})
		assert.Equal(t, "User", typeData["name"])
		assert.Equal(t, "OBJECT", typeData["kind"])
		assert.NotEmpty(t, typeData["fields"])
	})

	t.Run("变量传name", func(t *testing.T) {
		result := executor.run(ctx, `query ($n: String!) { __type(name: $n) { name } }`,
			map[string]interface{}{"n": "Post"}, "")
		assert.Empty(t, result.Errors)
		assert.Equal(t, "Post", result.Data["__type"].(map[string]interface{})["name"])
	})

	t.Run("未知类型返回null", func(t *testing.T) {
		result := executor.run(ctx, `{ __type(name: "Nope") { name } }`, nil, "")
		assert.Empty(t, result.Errors)
		assert.Nil(t, result.Data["__type"])
	})
}

func TestConditionalDirectivesSkipResolverAndFragments(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	calls := 0
	resolver := NewResolver[Root, struct{}, string]("Query", "greeting", "问候", func(context.Context, Root, struct{}) (string, error) {
		calls++
		return "hello", nil
	})
	assert.NoError(t, executor.Register(resolver))

	result := executor.run(context.Background(), `query($show: Boolean = false) {
		greeting @include(if: $show)
		...Greeting
	}
	fragment Greeting on Query { greeting @skip(if: true) }`, nil, "")
	assert.Empty(t, result.Errors)
	assert.Empty(t, result.Data)
	assert.Zero(t, calls, "被@include跳过的Resolver不得调用")

	result = executor.run(context.Background(), `query($show: Boolean!) {
		... on Query @include(if: $show) { greeting }
	}`, map[string]interface{}{"show": true}, "")
	assert.Empty(t, result.Errors)
	assert.Equal(t, "hello", result.Data["greeting"])
	assert.Equal(t, 1, calls)

	result = executor.run(context.Background(), `query { greeting @defer }`, nil, "")
	assert.NotEmpty(t, result.Errors, "未实现的@defer必须在validation阶段失败")

	result = executor.run(context.Background(), `{ __schema { directives { name } } }`, nil, "")
	assert.Empty(t, result.Errors)
	for _, item := range result.Data["__schema"].(map[string]interface{})["directives"].([]interface{}) {
		assert.NotEqual(t, "defer", item.(map[string]interface{})["name"], "Schema重建后不得重新暴露@defer")
	}
}

func TestConditionalDirectiveChangesSQLShape(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	query := `query($show: Boolean = false) { users { items { id name @include(if: $show) } } }`
	for _, tc := range []struct {
		name string
		vars map[string]interface{}
		want string
		have string
	}{
		{name: "omitted default false", vars: nil, want: `"id"`, have: `"name"`},
		{name: "variable true", vars: map[string]interface{}{"show": true}, want: `"name"`, have: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := executor.plan(query, "", tc.vars)
			assert.NoError(t, err)
			assert.Contains(t, plan.SQL, tc.want)
			if tc.have != "" {
				assert.NotContains(t, plan.SQL, tc.have)
			}
		})
	}
	first, err := executor.plan(query, "", map[string]interface{}{"show": true})
	assert.NoError(t, err)
	second, err := executor.plan(query, "", map[string]interface{}{"show": true})
	assert.NoError(t, err)
	assert.Same(t, first, second, "相同directive选择形状必须复用编译计划")

	ignoredTokens := `query($show: Boolean!) { users { items { id name @ # ignored token
		include(if: $show) } } }`
	hidden, err := executor.plan(ignoredTokens, "", map[string]interface{}{"show": false})
	assert.NoError(t, err)
	assert.NotContains(t, hidden.SQL, `"name"`)
	shown, err := executor.plan(ignoredTokens, "", map[string]interface{}{"show": true})
	assert.NoError(t, err)
	assert.Contains(t, shown.SQL, `"name"`, "指令名与@之间的GraphQL忽略token不得绕过计划分形")
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
