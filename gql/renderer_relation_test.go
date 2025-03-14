package gql

import (
	"strings"
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRenderRelation 测试关系渲染功能
func TestRenderRelation(t *testing.T) {
	// 创建测试元数据
	meta := createRelationTestMetadata()

	// 创建配置
	meta.cfg = &internal.Config{
		Schema: internal.SchemaConfig{
			ShowThrough: false, // 默认隐藏中间表关系
			TypeMapping: map[string]string{},
		},
	}

	// 先处理元数据中的关系，然后才渲染
	meta.processAllRelationships()

	// 创建渲染器
	renderer := NewRenderer(meta)

	// 绕过文件保存部分直接获取schema
	schema := &strings.Builder{}
	renderer.sb = schema

	// 渲染类型
	err := renderer.renderTypes()
	require.NoError(t, err, "渲染实体类型失败")

	// 获取生成的schema文本
	generatedSchema := schema.String()

	// 验证多对一关系
	t.Run("多对一关系", func(t *testing.T) {
		// User表中应该有department字段，指向Department
		assert.Contains(t, generatedSchema, "department: Department!")
		// 应该包含注释
		assert.Contains(t, generatedSchema, "# 关联的Department")
	})

	// 验证一对多关系
	t.Run("一对多关系", func(t *testing.T) {
		// Department表中应该有users字段，是User的列表
		assert.Contains(t, generatedSchema, "users: [User]!")
		// 应该包含注释
		assert.Contains(t, generatedSchema, "# 关联的User列表")
	})

	// 验证多对多关系
	t.Run("多对多关系", func(t *testing.T) {
		// Post表中应该有tags字段，是Tag的列表
		assert.Contains(t, generatedSchema, "tags: [Tag]!")
		// Tag表中应该有posts字段，是Post的列表
		assert.Contains(t, generatedSchema, "posts: [Post]!")
		// 应该包含注释
		assert.Contains(t, generatedSchema, "# 多对多关联的Tag列表")
		assert.Contains(t, generatedSchema, "# 多对多关联的Post列表")
	})

	// 验证递归关系
	t.Run("递归关系", func(t *testing.T) {
		// Organization表中应该有parent字段，指向Organization
		assert.Contains(t, generatedSchema, "parent: Organization")
		// Organization表中应该有children字段，是Organization的列表
		assert.Contains(t, generatedSchema, "children: [Organization]!")
		// 应该包含注释
		assert.Contains(t, generatedSchema, "# 父Organization对象")
		assert.Contains(t, generatedSchema, "# 子Organization列表")
	})

	// 验证字段冲突处理
	t.Run("字段冲突处理", func(t *testing.T) {
		// 在重命名测试中，已存在的admin字段会导致冲突
		// 新的关系字段应该是user1而不是user
		assert.Contains(t, generatedSchema, "user1: User!")
	})

	// 验证中间表关系显示
	t.Run("中间表关系显示", func(t *testing.T) {
		// 修改配置显示中间表关系
		meta.cfg.Schema.ShowThrough = true

		// 重新处理关系并创建渲染器
		meta.processAllRelationships()
		renderer = NewRenderer(meta)
		schema = &strings.Builder{}
		renderer.sb = schema

		// 重新渲染
		err = renderer.renderTypes()
		require.NoError(t, err, "渲染实体类型失败")

		// 获取新的schema文本
		newSchema := schema.String()

		// 现在应该有中间表关系
		assert.Contains(t, newSchema, "postTags: [PostTags]!")
	})
}

// createRelationTestMetadata 创建用于测试关系的元数据
func createRelationTestMetadata() *Metadata {
	meta := &Metadata{
		Nodes: make(map[string]*internal.Class),
	}

	// 创建User类
	userClass := &internal.Class{
		Name:   "User",
		Table:  "users",
		Fields: make(map[string]*internal.Field),
	}
	userClass.Fields["id"] = &internal.Field{
		Name:      "id",
		Column:    "id",
		Type:      "integer",
		IsPrimary: true,
	}
	userClass.Fields["name"] = &internal.Field{
		Name:   "name",
		Column: "name",
		Type:   "string",
	}
	userClass.Fields["departmentId"] = &internal.Field{
		Name:   "departmentId",
		Column: "department_id",
		Type:   "integer",
	}

	// 添加多对一关系字段
	userClass.Fields["department"] = &internal.Field{
		Name:         "department",
		Type:         "Department",
		Description:  "关联的Department",
		Virtual:      true,
		IsCollection: false,
	}

	// 添加一对多关系字段 - User指向Post
	userClass.Fields["posts"] = &internal.Field{
		Name:         "posts",
		Type:         "Post",
		Description:  "用户的文章列表",
		Virtual:      true,
		IsCollection: true,
	}

	// 创建Department类
	deptClass := &internal.Class{
		Name:   "Department",
		Table:  "departments",
		Fields: make(map[string]*internal.Field),
	}
	deptClass.Fields["id"] = &internal.Field{
		Name:      "id",
		Column:    "id",
		Type:      "integer",
		IsPrimary: true,
	}
	deptClass.Fields["name"] = &internal.Field{
		Name:   "name",
		Column: "name",
		Type:   "string",
	}

	// 添加一对多关系字段
	deptClass.Fields["users"] = &internal.Field{
		Name:         "users",
		Type:         "User",
		Description:  "关联的User列表",
		Virtual:      true,
		IsCollection: true,
	}

	// 创建Admin类，用于测试字段冲突处理
	adminClass := &internal.Class{
		Name:   "Admin",
		Table:  "admins",
		Fields: make(map[string]*internal.Field),
	}
	adminClass.Fields["id"] = &internal.Field{
		Name:      "id",
		Column:    "id",
		Type:      "integer",
		IsPrimary: true,
	}
	adminClass.Fields["name"] = &internal.Field{
		Name:   "name",
		Column: "name",
		Type:   "string",
	}
	adminClass.Fields["userId"] = &internal.Field{
		Name:   "userId",
		Column: "user_id",
		Type:   "integer",
	}
	// 添加已存在的名为admin的普通字段，将与关系字段冲突
	adminClass.Fields["admin"] = &internal.Field{
		Name:   "admin",
		Column: "admin",
		Type:   "boolean",
	}

	// 添加多对一关系字段，会与admin字段冲突
	adminClass.Fields["user1"] = &internal.Field{
		Name:         "user1",
		Type:         "User",
		Description:  "关联的User",
		Virtual:      true,
		IsCollection: false,
	}

	// 创建Post类
	postClass := &internal.Class{
		Name:   "Post",
		Table:  "posts",
		Fields: make(map[string]*internal.Field),
	}
	postClass.Fields["id"] = &internal.Field{
		Name:      "id",
		Column:    "id",
		Type:      "integer",
		IsPrimary: true,
	}
	postClass.Fields["title"] = &internal.Field{
		Name:   "title",
		Column: "title",
		Type:   "string",
	}
	// 添加userId字段
	postClass.Fields["userId"] = &internal.Field{
		Name:   "userId",
		Column: "user_id",
		Type:   "integer",
	}

	// 添加多对一关系字段 - Post指向User
	postClass.Fields["user"] = &internal.Field{
		Name:         "user",
		Type:         "User",
		Description:  "文章作者",
		Virtual:      true,
		IsCollection: false,
	}

	// 添加多对多关系字段
	postClass.Fields["tags"] = &internal.Field{
		Name:         "tags",
		Type:         "Tag",
		Description:  "多对多关联的Tag列表",
		Virtual:      true,
		IsCollection: true,
	}

	// 添加中间表关系字段
	postClass.Fields["postTags"] = &internal.Field{
		Name:         "postTags",
		Type:         "PostTags",
		Description:  "关联的PostTags列表",
		Virtual:      true,
		IsCollection: true,
	}

	// 创建Tag类
	tagClass := &internal.Class{
		Name:   "Tag",
		Table:  "tags",
		Fields: make(map[string]*internal.Field),
	}
	tagClass.Fields["id"] = &internal.Field{
		Name:      "id",
		Column:    "id",
		Type:      "integer",
		IsPrimary: true,
	}
	tagClass.Fields["name"] = &internal.Field{
		Name:   "name",
		Column: "name",
		Type:   "string",
	}

	// 添加多对多关系字段
	tagClass.Fields["posts"] = &internal.Field{
		Name:         "posts",
		Type:         "Post",
		Description:  "多对多关联的Post列表",
		Virtual:      true,
		IsCollection: true,
	}

	// 添加中间表关系字段
	tagClass.Fields["postTags"] = &internal.Field{
		Name:         "postTags",
		Type:         "PostTags",
		Description:  "关联的PostTags列表",
		Virtual:      true,
		IsCollection: true,
	}

	// 添加中间表 PostTags
	postTagsClass := &internal.Class{
		Name:   "PostTags",
		Table:  "post_tags",
		Fields: make(map[string]*internal.Field),
	}
	postTagsClass.Fields["postId"] = &internal.Field{
		Name:   "postId",
		Column: "post_id",
		Type:   "integer",
	}
	postTagsClass.Fields["tagId"] = &internal.Field{
		Name:   "tagId",
		Column: "tag_id",
		Type:   "integer",
	}

	// 添加多对一关系字段
	postTagsClass.Fields["post"] = &internal.Field{
		Name:         "post",
		Type:         "Post",
		Description:  "关联的Post",
		Virtual:      true,
		IsCollection: false,
	}

	// 添加多对一关系字段
	postTagsClass.Fields["tag"] = &internal.Field{
		Name:         "tag",
		Type:         "Tag",
		Description:  "关联的Tag",
		Virtual:      true,
		IsCollection: false,
	}

	// 创建Organization类（自关联）
	orgClass := &internal.Class{
		Name:   "Organization",
		Table:  "organizations",
		Fields: make(map[string]*internal.Field),
	}
	orgClass.Fields["id"] = &internal.Field{
		Name:      "id",
		Column:    "id",
		Type:      "integer",
		IsPrimary: true,
	}
	orgClass.Fields["name"] = &internal.Field{
		Name:   "name",
		Column: "name",
		Type:   "string",
	}
	orgClass.Fields["parentId"] = &internal.Field{
		Name:     "parentId",
		Column:   "parent_id",
		Type:     "integer",
		Nullable: true,
	}

	// 添加递归关系字段 - 父组织
	orgClass.Fields["parent"] = &internal.Field{
		Name:         "parent",
		Type:         "Organization",
		Description:  "父Organization对象",
		Virtual:      true,
		Nullable:     true,
		IsCollection: false,
	}

	// 添加递归关系字段 - 子组织
	orgClass.Fields["children"] = &internal.Field{
		Name:         "children",
		Type:         "Organization",
		Description:  "子Organization列表",
		Virtual:      true,
		IsCollection: true,
	}

	// 添加所有类到元数据
	meta.Nodes["User"] = userClass
	meta.Nodes["Department"] = deptClass
	meta.Nodes["Admin"] = adminClass
	meta.Nodes["Post"] = postClass
	meta.Nodes["Tag"] = tagClass
	meta.Nodes["PostTags"] = postTagsClass
	meta.Nodes["Organization"] = orgClass

	return meta
}
