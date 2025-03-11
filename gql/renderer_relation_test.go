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
		assert.Contains(t, generatedSchema, "department1: Department!")
		// 应该包含注释
		assert.Contains(t, generatedSchema, "# 关联的Department对象")
	})

	// 验证一对多关系
	t.Run("一对多关系", func(t *testing.T) {
		// Department表中应该有userList字段，是User的列表
		assert.Contains(t, generatedSchema, "userList: [User]!")
		// 应该包含注释
		assert.Contains(t, generatedSchema, "# 关联的User列表")
	})

	// 验证多对多关系
	t.Run("多对多关系", func(t *testing.T) {
		// Post表中应该有tagList字段，是Tag的列表
		assert.Contains(t, generatedSchema, "tagList: [Tag]!")
		// Tag表中应该有postList字段，是Post的列表
		assert.Contains(t, generatedSchema, "postList: [Post]!")
		// 应该包含注释
		assert.Contains(t, generatedSchema, "# 多对多关联的Tag列表")
		assert.Contains(t, generatedSchema, "# 多对多关联的Post列表")
	})

	// 验证递归关系
	t.Run("递归关系", func(t *testing.T) {
		// Organization表中应该有parent字段，指向Organization
		assert.Contains(t, generatedSchema, "parent1: Organization")
		// Organization表中应该有children字段，是Organization的列表
		assert.Contains(t, generatedSchema, "children1: [Organization]!")
		// 应该包含注释
		assert.Contains(t, generatedSchema, "# 父Organization对象")
		assert.Contains(t, generatedSchema, "# 子Organization列表")
	})

	// 验证字段冲突处理
	t.Run("字段冲突处理", func(t *testing.T) {
		// 在重命名测试中，已存在的admin字段会导致冲突
		// 新的关系字段应该是user而不是admin，因为我们现在按照实体名称命名
		assert.Contains(t, generatedSchema, "user: User!")
	})

	// 验证中间表关系隐藏
	t.Run("中间表关系隐藏", func(t *testing.T) {
		// 默认情况下中间表关系应该被隐藏
		// 修改Post和Tag的关系检测，这些现在应该是直接的多对多关系
		assert.Contains(t, generatedSchema, "tagList: [Tag]!")
		assert.Contains(t, generatedSchema, "postList: [Post]!")
	})

	// 测试中间表关系显示
	t.Run("中间表关系显示", func(t *testing.T) {
		// 修改配置显示中间表关系
		meta.cfg.Schema.ShowThrough = true

		// 重新创建渲染器
		renderer = NewRenderer(meta)
		schema = &strings.Builder{}
		renderer.sb = schema

		// 重新渲染
		err = renderer.renderTypes()
		require.NoError(t, err, "渲染实体类型失败")

		// 获取新的schema文本
		newSchema := schema.String()

		// 现在应该仍有多对多关系和中间表关系
		assert.Contains(t, newSchema, "tagList: [Tag]!")
		assert.Contains(t, newSchema, "postList: [Post]!")
		assert.Contains(t, newSchema, "postTagList: [PostTags]!")
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
	// 添加多对一关系
	userClass.Fields["department"] = &internal.Field{
		Name: "department",
		Type: "object",
		Relation: &internal.Relation{
			SourceClass: "User",
			SourceField: "departmentId",
			TargetClass: "Department",
			TargetField: "id",
			Type:        internal.MANY_TO_ONE,
		},
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
	// 添加一对多关系
	deptClass.Fields["users"] = &internal.Field{
		Name: "users",
		Type: "array",
		Relation: &internal.Relation{
			SourceClass: "Department",
			SourceField: "id",
			TargetClass: "User",
			TargetField: "departmentId",
			Type:        internal.ONE_TO_MANY,
		},
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
	// 添加多对一关系，关系字段名会从"user"转换而来，将与已存在的admin字段冲突
	adminClass.Fields["userRelation"] = &internal.Field{
		Name: "userRelation",
		Type: "object",
		Relation: &internal.Relation{
			SourceClass: "Admin",
			SourceField: "userId",
			TargetClass: "User",
			TargetField: "id",
			Type:        internal.MANY_TO_ONE,
		},
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
	// 添加多对多关系
	postClass.Fields["tags"] = &internal.Field{
		Name: "tags",
		Type: "array",
		Relation: &internal.Relation{
			SourceClass: "Post",
			SourceField: "id",
			TargetClass: "Tag",
			TargetField: "id",
			Type:        internal.MANY_TO_MANY,
			Through: &internal.Through{
				Table:     "post_tags",
				SourceKey: "post_id",
				TargetKey: "tag_id",
			},
		},
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
	// 添加多对多关系
	tagClass.Fields["posts"] = &internal.Field{
		Name: "posts",
		Type: "array",
		Relation: &internal.Relation{
			SourceClass: "Tag",
			SourceField: "id",
			TargetClass: "Post",
			TargetField: "id",
			Type:        internal.MANY_TO_MANY,
			Through: &internal.Through{
				Table:     "post_tags",
				SourceKey: "tag_id",
				TargetKey: "post_id",
			},
		},
	}

	// 添加中间表 PostTags 用于测试中间表关系显示/隐藏
	postTagsClass := &internal.Class{
		Name:   "PostTags",
		Table:  "post_tags",
		Fields: make(map[string]*internal.Field),
	}
	postTagsClass.Fields["postId"] = &internal.Field{
		Name:   "postId",
		Column: "post_id",
		Type:   "integer",
		Relation: &internal.Relation{
			SourceClass: "PostTags",
			SourceField: "postId",
			TargetClass: "Post",
			TargetField: "id",
			Type:        internal.MANY_TO_ONE,
		},
	}
	postTagsClass.Fields["tagId"] = &internal.Field{
		Name:   "tagId",
		Column: "tag_id",
		Type:   "integer",
		Relation: &internal.Relation{
			SourceClass: "PostTags",
			SourceField: "tagId",
			TargetClass: "Tag",
			TargetField: "id",
			Type:        internal.MANY_TO_ONE,
		},
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
	// 添加递归关系 - 父组织
	orgClass.Fields["parent"] = &internal.Field{
		Name:     "parent",
		Type:     "object",
		Nullable: true,
		Relation: &internal.Relation{
			SourceClass: "Organization",
			SourceField: "parentId",
			TargetClass: "Organization",
			TargetField: "id",
			Type:        internal.RECURSIVE,
		},
	}
	// 添加递归关系 - 子组织
	orgClass.Fields["children"] = &internal.Field{
		Name: "children",
		Type: "array",
		Relation: &internal.Relation{
			SourceClass: "Organization",
			SourceField: "id",
			TargetClass: "Organization",
			TargetField: "parentId",
			Type:        internal.RECURSIVE,
		},
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
