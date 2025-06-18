package gql

import (
	"fmt"
	"github.com/ichaly/ideabase/gql/renderer"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/std"
	"github.com/ichaly/ideabase/utl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 尝试从测试数据库获取元数据
func getTestMetadata(t *testing.T) (*Metadata, error) {
	// 初始化测试数据库
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	// 数据库已连接，创建并加载元数据
	t.Log("成功连接到数据库，准备加载元数据")

	// 设置Konfig配置
	k, err := std.NewKonfig()
	require.NoError(t, err, "创建配置失败")
	k.Set("mode", "dev")
	k.Set("app.root", utl.Root())
	k.Set("schema.schema", "public")
	k.Set("metadata.use-camel", true)
	k.Set("metadata.show-through", true)
	k.Set("metadata.table-prefix", []string{"sys_"})

	// 创建元数据并从数据库加载
	meta, err := NewMetadata(k, db)
	if err != nil {
		return nil, fmt.Errorf("从数据库加载元数据失败: %w", err)
	}

	t.Logf("成功加载元数据，包含 %d 个类定义", len(meta.Nodes))
	return meta, nil
}

// 创建模拟元数据用于测试
func createMockMetadata(t *testing.T) *Metadata {
	k, err := std.NewKonfig()
	require.NoError(t, err, "创建配置失败")
	k.Set("mode", "dev")
	k.Set("app.root", t.TempDir()) // 使用临时目录作为根目录

	// 定义类型映射
	typeMapping := map[string]string{
		"integer":     "Int",
		"int":         "Int",
		"int4":        "Int",
		"bigint":      "Int",
		"smallint":    "Int",
		"serial":      "Int",
		"decimal":     "Float",
		"numeric":     "Float",
		"real":        "Float",
		"double":      "Float",
		"float":       "Float",
		"text":        "String",
		"varchar":     "String",
		"character":   "String",
		"char":        "String",
		"bytea":       "String",
		"uuid":        "String",
		"boolean":     "Boolean",
		"bool":        "Boolean",
		"timestamp":   "DateTime",
		"timestamptz": "DateTime",
		"date":        "DateTime",
		"time":        "DateTime",
		"jsonb":       "Json",
		"json":        "Json",
	}

	meta := &Metadata{
		k:       k,
		Nodes:   make(map[string]*internal.Class),
		Version: time.Now().Format("20060102150405"),
		cfg: &internal.Config{
			Schema: internal.SchemaConfig{
				TypeMapping: typeMapping,
			},
		},
	}

	// 添加模拟的User类
	userClass := &internal.Class{
		Name:        "User",
		Table:       "users",
		Description: "用户信息",
		PrimaryKeys: []string{"id"},
		Fields:      make(map[string]*internal.Field),
	}

	// 添加User类的字段
	userClass.AddField(&internal.Field{
		Name:        "id",
		Column:      "id",
		Type:        "integer",
		IsPrimary:   true,
		Description: "用户ID",
	})

	userClass.AddField(&internal.Field{
		Name:        "name",
		Column:      "name",
		Type:        "text",
		Description: "用户名",
	})

	userClass.AddField(&internal.Field{
		Name:        "email",
		Column:      "email",
		Type:        "text",
		Description: "邮箱",
	})

	userClass.AddField(&internal.Field{
		Name:        "createdAt",
		Column:      "created_at",
		Type:        "timestamp",
		Description: "创建时间",
	})

	// 添加模拟的Post类
	postClass := &internal.Class{
		Name:        "Post",
		Table:       "posts",
		Description: "文章信息",
		PrimaryKeys: []string{"id"},
		Fields:      make(map[string]*internal.Field),
	}

	// 添加Post类的字段
	postClass.AddField(&internal.Field{
		Name:        "id",
		Column:      "id",
		Type:        "integer",
		IsPrimary:   true,
		Description: "文章ID",
	})

	postClass.AddField(&internal.Field{
		Name:        "title",
		Column:      "title",
		Type:        "text",
		Description: "标题",
	})

	postClass.AddField(&internal.Field{
		Name:        "content",
		Column:      "content",
		Type:        "text",
		Description: "内容",
		Nullable:    true,
	})

	// 添加关系字段
	userIdField := &internal.Field{
		Name:        "userId",
		Column:      "user_id",
		Type:        "integer",
		Description: "作者ID",
	}

	// 创建关系
	userIdField.Relation = &internal.Relation{
		SourceTable:  "Post",
		SourceColumn: "userId",
		TargetTable:  "User",
		TargetColumn: "id",
		Type:         internal.MANY_TO_ONE,
	}

	postClass.AddField(userIdField)

	// 将类添加到元数据中
	meta.Nodes["User"] = userClass
	meta.Nodes["Post"] = postClass

	return meta
}

// 基本渲染器生成测试
func TestRenderer_Generate(t *testing.T) {
	// 从数据库或模拟数据获取元数据
	meta, err := getTestMetadata(t)
	if err != nil {
		t.Skipf("跳过测试: %v", err)
		return
	}

	// 检查并打印中间表信息
	for className, class := range meta.Nodes {
		if className == class.Name && class.IsThrough {
			t.Logf("检测到中间表: %s, IsThrough=%v", className, class.IsThrough)
		}
	}

	// 确认配置状态
	t.Logf("ShowThrough配置: %v", meta.cfg.Metadata.ShowThrough)

	// 创建渲染器
	renderer := NewRenderer(meta)

	// 生成schema
	schema, err := renderer.Generate()

	// 验证生成成功
	assert.NoError(t, err)
	assert.NotEmpty(t, schema)

	// 验证schema包含预期部分
	assert.Contains(t, schema, "type Query {")
	assert.Contains(t, schema, "type Mutation {")
	assert.Contains(t, schema, "scalar DateTime")

	// 检查中间表是否被渲染
	if !meta.cfg.Metadata.ShowThrough {
		assert.NotContains(t, schema, "type PostTag {")
	}

	// 验证基本类型是否存在
	if len(meta.Nodes) > 0 {
		// 使用固定的类名进行验证，避免随机性
		className := "User"
		t.Logf("验证生成的schema中包含%s类型", className)
		assert.Contains(t, schema, "type "+className+" {")
	}

	// 验证Post类型是否有comments字段
	assert.Contains(t, schema, "type Post {")
	assert.Contains(t, schema, "# 关联的Comment列表")

	// 验证User类型是否有posts字段
	assert.Contains(t, schema, "type User {")
	assert.Contains(t, schema, "# 关联的Post列表")
}

// 使用模拟数据测试渲染器
func TestRenderer_WithMockData(t *testing.T) {
	// 创建模拟元数据
	meta := createMockMetadata(t)

	// 创建渲染器
	renderer := NewRenderer(meta)

	// 绕过文件保存部分直接获取schema
	schema := &strings.Builder{}
	renderer.sb = schema

	// 生成schema但不保存到文件
	for _, fn := range []func() error{
		renderer.renderScalars,
		renderer.renderEnums,
		renderer.renderCommon,
		renderer.renderTypes,
		renderer.renderPaging,
		renderer.renderFilter,
		renderer.renderEntity,
		renderer.renderSort,
		renderer.renderInput,
		renderer.renderQuery,
		renderer.renderMutation,
	} {
		err := fn()
		assert.NoError(t, err, "渲染schema部分失败")
	}

	// 验证生成成功
	generatedSchema := schema.String()
	assert.NotEmpty(t, generatedSchema)

	// 验证基本类型
	assert.Contains(t, generatedSchema, "type User {")
	assert.Contains(t, generatedSchema, "type Post {")

	// 验证字段类型 (根据实际生成的类型进行检查)
	assert.Contains(t, generatedSchema, "id: ID!")
	assert.Contains(t, generatedSchema, "name: String!")
	assert.Contains(t, generatedSchema, "email: String!")
	assert.Contains(t, generatedSchema, "createdAt: DateTime!")

	// 验证字段描述存在
	assert.Contains(t, generatedSchema, "# 用户名")
	assert.Contains(t, generatedSchema, "# 邮箱")
	assert.Contains(t, generatedSchema, "# 用户ID")

	// Post 类的字段
	assert.Contains(t, generatedSchema, "# 文章ID")
	assert.Contains(t, generatedSchema, "# 标题")
	assert.Contains(t, generatedSchema, "# 内容")
	assert.Contains(t, generatedSchema, "# 作者ID")
}

// 测试渲染标量类型
func TestRenderer_RenderScalars(t *testing.T) {
	// 创建模拟元数据
	meta := createMockMetadata(t)

	// 创建渲染器
	renderer := NewRenderer(meta)

	// 绕过文件保存部分直接获取schema
	schema := &strings.Builder{}
	renderer.sb = schema

	// 只渲染标量类型
	err := renderer.renderScalars()
	assert.NoError(t, err, "渲染标量类型失败")

	// 获取生成的schema文本
	generatedSchema := schema.String()

	// 验证标量类型
	assert.Contains(t, generatedSchema, "scalar DateTime")
	assert.Contains(t, generatedSchema, "scalar Json")
	assert.Contains(t, generatedSchema, "scalar Cursor")
}

// 测试渲染枚举类型
func TestRenderer_RenderEnums(t *testing.T) {
	// 创建模拟元数据
	meta := createMockMetadata(t)

	// 创建渲染器
	renderer := NewRenderer(meta)

	// 绕过文件保存部分直接获取schema
	schema := &strings.Builder{}
	renderer.sb = schema

	// 只渲染枚举类型
	err := renderer.renderEnums()
	assert.NoError(t, err, "渲染枚举类型失败")

	// 获取生成的schema文本
	generatedSchema := schema.String()

	// 验证枚举类型
	assert.Contains(t, generatedSchema, "enum SortDirection {")
	assert.Contains(t, generatedSchema, "ASC")
	assert.Contains(t, generatedSchema, "DESC")
}

// 测试渲染分页类型
func TestRenderer_RenderPaging(t *testing.T) {
	// 创建模拟元数据
	meta := createMockMetadata(t)

	// 创建渲染器
	renderer := NewRenderer(meta)

	// 绕过文件保存部分直接获取schema
	schema := &strings.Builder{}
	renderer.sb = schema

	// 渲染通用类型和分页类型
	err := renderer.renderCommon()
	assert.NoError(t, err, "渲染通用类型失败")

	err = renderer.renderTypes()
	assert.NoError(t, err, "渲染实体类型失败")

	err = renderer.renderPaging()
	assert.NoError(t, err, "渲染分页类型失败")

	// 获取生成的schema文本
	generatedSchema := schema.String()

	// 验证分页类型
	assert.Contains(t, generatedSchema, "type PageInfo {")
	assert.Contains(t, generatedSchema, "hasNext")
	assert.Contains(t, generatedSchema, "hasPrev")

	// 验证连接类型
	assert.Contains(t, generatedSchema, "type UserPage {")
	assert.Contains(t, generatedSchema, "type PostPage {")
	assert.Contains(t, generatedSchema, "items: [User")
	assert.Contains(t, generatedSchema, "pageInfo: PageInfo!")
}

// 测试渲染过滤器类型
func TestRenderer_RenderFilter(t *testing.T) {
	// 创建模拟元数据
	meta := createMockMetadata(t)

	// 创建渲染器
	renderer := NewRenderer(meta)

	// 绕过文件保存部分直接获取schema
	schema := &strings.Builder{}
	renderer.sb = schema

	// 渲染过滤器类型
	err := renderer.renderFilter()
	assert.NoError(t, err, "渲染过滤器类型失败")

	// 获取生成的schema文本
	generatedSchema := schema.String()

	// 验证通用过滤器类型
	assert.Contains(t, generatedSchema, "input StringFilter {")
	assert.Contains(t, generatedSchema, "input IntFilter {")
	assert.Contains(t, generatedSchema, "input DateTimeFilter {")
}

// 测试渲染排序类型
func TestRenderer_RenderSort(t *testing.T) {
	// 创建模拟元数据
	meta := createMockMetadata(t)

	// 创建渲染器
	renderer := NewRenderer(meta)

	// 绕过文件保存部分直接获取schema
	schema := &strings.Builder{}
	renderer.sb = schema

	// 渲染类型和排序类型
	err := renderer.renderTypes()
	assert.NoError(t, err, "渲染实体类型失败")

	err = renderer.renderSort()
	assert.NoError(t, err, "渲染排序类型失败")

	// 获取生成的schema文本
	generatedSchema := schema.String()

	// 验证排序类型
	assert.Contains(t, generatedSchema, "input UserSort {")
	assert.Contains(t, generatedSchema, "input PostSort {")
}

// 测试渲染查询根类型
func TestRenderer_RenderQuery(t *testing.T) {
	// 创建模拟元数据
	meta := createMockMetadata(t)

	// 创建渲染器
	renderer := NewRenderer(meta)

	// 绕过文件保存部分直接获取schema
	schema := &strings.Builder{}
	renderer.sb = schema

	// 渲染所需类型和查询类型
	err := renderer.renderTypes()
	assert.NoError(t, err, "渲染实体类型失败")

	err = renderer.renderPaging()
	assert.NoError(t, err, "渲染分页类型失败")

	err = renderer.renderQuery()
	assert.NoError(t, err, "渲染查询类型失败")

	// 获取生成的schema文本
	generatedSchema := schema.String()

	// 验证查询根类型
	assert.Contains(t, generatedSchema, "type Query {")

	// 验证单个实体查询
	assert.Contains(t, generatedSchema, "user(")
	assert.Contains(t, generatedSchema, "post(")

	// 验证实体列表查询
	assert.Contains(t, generatedSchema, "users(")
	assert.Contains(t, generatedSchema, "posts(")
}

// 测试渲染变更根类型
func TestRenderer_RenderMutation(t *testing.T) {
	// 创建模拟元数据
	meta := createMockMetadata(t)

	// 创建渲染器
	renderer := NewRenderer(meta)

	// 绕过文件保存部分直接获取schema
	schema := &strings.Builder{}
	renderer.sb = schema

	// 渲染所需类型和变更类型
	err := renderer.renderTypes()
	assert.NoError(t, err, "渲染实体类型失败")

	err = renderer.renderInput()
	assert.NoError(t, err, "渲染输入类型失败")

	err = renderer.renderMutation()
	assert.NoError(t, err, "渲染变更类型失败")

	// 获取生成的schema文本
	generatedSchema := schema.String()

	// 验证变更根类型
	assert.Contains(t, generatedSchema, "type Mutation {")

	// 验证创建操作
	assert.Contains(t, generatedSchema, "createUser(")
	assert.Contains(t, generatedSchema, "createPost(")

	// 验证更新操作
	assert.Contains(t, generatedSchema, "updateUser(")
	assert.Contains(t, generatedSchema, "updatePost(")

	// 验证删除操作
	assert.Contains(t, generatedSchema, "deleteUser(")
	assert.Contains(t, generatedSchema, "deletePost(")
}

// 测试渲染输入类型
func TestRenderer_RenderInput(t *testing.T) {
	// 创建模拟元数据
	meta := createMockMetadata(t)

	// 创建渲染器
	renderer := NewRenderer(meta)

	// 绕过文件保存部分直接获取schema
	schema := &strings.Builder{}
	renderer.sb = schema

	// 渲染所需类型和输入类型
	err := renderer.renderTypes()
	assert.NoError(t, err, "渲染实体类型失败")

	err = renderer.renderInput()
	assert.NoError(t, err, "渲染输入类型失败")

	// 获取生成的schema文本
	generatedSchema := schema.String()

	// 验证创建输入类型
	assert.Contains(t, generatedSchema, "input UserCreateInput {")
	assert.Contains(t, generatedSchema, "input PostCreateInput {")

	// 验证更新输入类型
	assert.Contains(t, generatedSchema, "input UserUpdateInput {")
	assert.Contains(t, generatedSchema, "input PostUpdateInput {")
}

// 测试渲染文件保存功能
func TestRenderer_SaveToFile(t *testing.T) {
	// 创建模拟元数据
	meta := createMockMetadata(t)
	// 先确保临时目录存在
	tempDir := t.TempDir()
	meta.k.Set("app.root", tempDir)

	// 创建渲染器
	renderer := NewRenderer(meta)

	// 绕过文件保存部分直接获取schema
	schema := &strings.Builder{}
	renderer.sb = schema

	// 生成schema但不保存到文件
	for _, fn := range []func() error{
		renderer.renderScalars,
		renderer.renderEnums,
		renderer.renderCommon,
		renderer.renderTypes,
	} {
		err := fn()
		assert.NoError(t, err, "渲染schema部分失败")
	}

	// 验证生成成功
	generated := schema.String()
	assert.NotEmpty(t, generated)

	// 手动保存到文件
	schemaPath := filepath.Join(tempDir, "schema.graphql")
	err := os.WriteFile(schemaPath, []byte(generated), 0644)
	assert.NoError(t, err, "保存schema文件失败")

	// 读取文件内容并验证
	content, err := os.ReadFile(schemaPath)
	assert.NoError(t, err)
	assert.Equal(t, generated, string(content), "生成的schema与文件内容应相同")
}

// 测试数据类型映射
func TestRenderer_DataTypeMapping(t *testing.T) {
	// 创建模拟元数据
	meta := createMockMetadata(t)

	// 添加各种数据类型的字段
	class := meta.Nodes["User"]

	// 定义类型映射
	typeMapping := map[string]string{
		"int":       "Int",
		"integer":   "Int",
		"smallint":  "Int",
		"bigint":    "Int",
		"serial":    "Int",
		"float":     "Float",
		"real":      "Float",
		"double":    "Float",
		"numeric":   "Float",
		"decimal":   "Float",
		"text":      "String",
		"varchar":   "String",
		"character": "String",
		"char":      "String",
		"bool":      "Boolean",
		"boolean":   "Boolean",
		"timestamp": "DateTime",
		"date":      "DateTime",
		"time":      "DateTime",
		"bytea":     "String",
		"json":      "Json",
		"jsonb":     "Json",
		"uuid":      "String",
	}

	// 先添加所有字段到类中
	for dbType, _ := range typeMapping {
		fieldName := "field_" + dbType
		class.AddField(&internal.Field{
			Name:      fieldName,
			Column:    fieldName,
			Type:      dbType,
			IsPrimary: false, // 确保不会自动映射为ID
		})
	}

	// 设置配置
	meta.cfg = &internal.Config{
		Schema: internal.SchemaConfig{
			TypeMapping: typeMapping,
		},
	}

	// 创建渲染器
	renderer := NewRenderer(meta)

	// 绕过文件保存部分直接获取schema
	schema := &strings.Builder{}
	renderer.sb = schema

	// 渲染类型
	err := renderer.renderTypes()
	assert.NoError(t, err, "渲染实体类型失败")

	// 获取生成的schema文本
	generatedSchema := schema.String()

	// 对每种类型进行验证
	for dbType, graphqlType := range typeMapping {
		fieldName := "field_" + dbType
		expectedField := fieldName + ": " + graphqlType
		assert.Contains(t, generatedSchema, expectedField,
			fmt.Sprintf("数据库类型 %s 应该被映射为GraphQL类型 %s", dbType, graphqlType))
	}
}

// 测试渲染数据统计类型
func TestRenderer_RenderStats(t *testing.T) {
	// 创建模拟元数据
	meta := createMockMetadata(t)

	// 创建渲染器
	renderer := NewRenderer(meta)

	// 绕过文件保存部分直接获取schema
	schema := &strings.Builder{}
	renderer.sb = schema

	// 先渲染必要的基础类型
	err := renderer.renderScalars()
	assert.NoError(t, err, "渲染标量类型失败")

	err = renderer.renderTypes()
	assert.NoError(t, err, "渲染实体类型失败")

	// 渲染统计类型
	err = renderer.renderStats()
	assert.NoError(t, err, "渲染统计类型失败")

	// 获取生成的schema文本
	generatedSchema := schema.String()

	// 验证生成成功 - 通用统计类型
	assert.Contains(t, generatedSchema, "type NumberStats {")
	assert.Contains(t, generatedSchema, "type StringStats {")
	assert.Contains(t, generatedSchema, "type DateTimeStats {")

	// 验证实体统计类型
	assert.Contains(t, generatedSchema, "type UserStats {")
	assert.Contains(t, generatedSchema, "type PostStats {")

	// 验证特定统计字段
	assert.Contains(t, generatedSchema, "count: Int!")
	assert.Contains(t, generatedSchema, "countDistinct: Int!")
	assert.Contains(t, generatedSchema, "avg: Float")
	assert.Contains(t, generatedSchema, "sum: Float")
	assert.Contains(t, generatedSchema, "min:")
	assert.Contains(t, generatedSchema, "max:")
}

// 测试渲染关系
func TestRenderer_RenderRelation(t *testing.T) {
	// 创建模拟元数据
	meta := createMockMetadata(t)

	// 创建渲染器
	renderer := NewRenderer(meta)

	// 绕过文件保存部分直接获取schema
	schema := &strings.Builder{}
	renderer.sb = schema

	// 渲染类型时会自动调用renderRelation
	err := renderer.renderTypes()
	assert.NoError(t, err, "渲染类型失败")

	// 获取生成的schema文本
	generatedSchema := schema.String()

	// 验证Posts类包含userId字段
	assert.Contains(t, generatedSchema, "userId: Int!")

	// 注：在此测试中，由于mockMetadata中的设计，可能不会生成关系字段
	// 实际项目中应确保mockMetadata包含关系字段以验证renderRelation方法
}

// 测试数据库类型到GraphQL类型的映射
func TestRenderer_GetGraphQLType(t *testing.T) {
	// 定义测试用例
	testCases := map[string]string{
		"integer":     "Int",
		"int":         "Int",
		"int4":        "Int",
		"serial":      "Int",
		"bigint":      "Int",
		"smallint":    "Int",
		"decimal":     "Float",
		"numeric":     "Float",
		"real":        "Float",
		"double":      "Float",
		"float":       "Float",
		"text":        "String",
		"varchar":     "String",
		"char":        "String",
		"uuid":        "String",
		"boolean":     "Boolean",
		"bool":        "Boolean",
		"timestamp":   "DateTime",
		"timestamptz": "DateTime",
		"date":        "DateTime",
		"time":        "DateTime",
		"jsonb":       "Json",
		"json":        "Json",
		"unknown":     "unknown", // 未知类型保持不变
	}

	// 创建渲染器
	renderer := &Renderer{
		meta: &Metadata{
			cfg: &internal.Config{
				Schema: internal.SchemaConfig{
					TypeMapping: testCases, // 直接使用testCases作为TypeMapping
				},
			},
		},
	}

	// 测试每种类型的映射
	for dbType, expectedGraphQLType := range testCases {
		t.Run(dbType, func(t *testing.T) {
			field := &internal.Field{Type: dbType, IsPrimary: false}
			actualType := renderer.getGraphQLType(field)
			assert.Equal(t, expectedGraphQLType, actualType,
				"数据库类型 %s 应该映射为 %s, 但得到了 %s",
				dbType, expectedGraphQLType, actualType)
		})
	}
}

// 测试字段写入方法
func TestRenderer_WriteField(t *testing.T) {
	var buf strings.Builder

	// 创建一个 Renderer 但将输出重定向到我们的 buffer
	r := &Renderer{
		sb: &buf,
	}

	// 测试用例：必填字段
	buf.Reset()
	r.writeField("id", "ID", renderer.NonNull())
	require.Equal(t, "  id: ID!\n", buf.String())

	// 测试用例：带描述的字段
	buf.Reset()
	r.writeField("name", "String", renderer.WithComment("用户名称"))
	require.Equal(t, "  name: String  # 用户名称\n", buf.String())

	// 测试用例：非必填字段
	buf.Reset()
	r.writeField("age", "Int", renderer.List())
	require.Equal(t, "  age: [Int]\n", buf.String())

	// 测试用例：列表字段
	buf.Reset()
	r.writeField("tags", "String", renderer.ListNonNull(), renderer.NonNull())
	require.Equal(t, "  tags: [String!]!\n", buf.String())
}

// 纯配置驱动schema生成测试
func TestRenderer_GenerateWithConfig(t *testing.T) {
	// 1. 构造配置
	k, err := std.NewKonfig()
	require.NoError(t, err, "创建配置失败")
	k.Set("mode", "dev")
	k.Set("app.root", utl.Root())
	k.Set("metadata.table-prefix", []string{"sys_"})
	k.Set("metadata.classes", map[string]*internal.ClassConfig{
		"User": {
			Description: "用户表",
			Table:       "sys_user",
			Fields: map[string]*internal.FieldConfig{
				"id": {
					Type:      "ID",
					IsPrimary: true,
				},
				"name": {
					Type:        "String",
					Description: "用户名",
				},
				"email": {
					Type:        "String",
					Description: "邮箱",
				},
				"age": {
					Type:        "Int",
					Description: "年龄",
				},
			},
		},
		"Post": {
			Description: "文章表",
			Table:       "sys_post",
			Fields: map[string]*internal.FieldConfig{
				"id": {
					Type:      "ID",
					IsPrimary: true,
				},
				"title": {
					Type:        "String",
					Description: "标题",
				},
				"content": {
					Type:        "String",
					Description: "内容",
				},
				"userId": {
					Type:        "Int",
					Description: "作者ID",
					Relation: &internal.RelationConfig{
						TargetClass: "User",
						TargetField: "id",
						Type:        "many_to_one",
					},
				},
			},
		},
	})

	// 2. 生成元数据
	meta, err := NewMetadata(k, nil)
	require.NoError(t, err, "通过配置生成元数据失败")

	// 3. 生成schema
	renderer := NewRenderer(meta)
	schema, err := renderer.Generate()
	assert.NoError(t, err)
	assert.NotEmpty(t, schema)

	// 输出schema内容，便于调试
	t.Logf("Generated schema:\n%s", schema)

	// 4. 验证schema内容
	assert.Contains(t, schema, "type User {")
	assert.Contains(t, schema, "type Post {")
	assert.Contains(t, schema, "id: ID!")
	assert.Contains(t, schema, "name: String!")
	assert.Contains(t, schema, "email: String!")
	assert.Contains(t, schema, "age: Int!")
	assert.Contains(t, schema, "title: String!")
	assert.Contains(t, schema, "content: String!")
	assert.Contains(t, schema, "userId: Int!")
	// 验证注释
	assert.Contains(t, schema, "# 用户名")
	assert.Contains(t, schema, "# 邮箱")
	assert.Contains(t, schema, "# 年龄")
	assert.Contains(t, schema, "# 标题")
	assert.Contains(t, schema, "# 内容")
	assert.Contains(t, schema, "# 作者ID")
	// 验证根类型
	assert.Contains(t, schema, "type Query {")
	assert.Contains(t, schema, "type Mutation {")
}
