package gql

import (
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/assert"
)

// TestCopyClassFields 测试copyClassFields方法
func TestCopyClassFields(t *testing.T) {
	// 初始化测试元数据对象
	k, err := std.NewKonfig()
	assert.NoError(t, err, "创建配置失败")

	meta := &Metadata{
		k:       k,
		Nodes:   make(map[string]*internal.Class),
		Version: "test",
	}

	t.Run("基本字段复制测试", func(t *testing.T) {
		// 准备测试数据
		sourceClass := &internal.Class{
			Name:   "SourceClass",
			Table:  "source_table",
			Fields: make(map[string]*internal.Field),
		}

		// 添加一些基本字段
		sourceClass.AddField(&internal.Field{
			Type:        "string",
			Name:        "name",
			Column:      "name",
			Description: "姓名字段",
			Nullable:    true,
			IsPrimary:   false,
		})

		sourceClass.AddField(&internal.Field{
			Type:        "int",
			Name:        "id",
			Column:      "id",
			Description: "ID字段",
			Nullable:    false,
			IsPrimary:   true,
		})

		targetClass := &internal.Class{
			Name:   "TargetClass",
			Table:  "target_table",
			Fields: make(map[string]*internal.Field),
		}

		// 执行复制操作
		meta.copyClassFields(targetClass, sourceClass)

		// 断言结果
		assert.Equal(t, 2, len(targetClass.Fields), "目标类应该有2个字段")

		// 验证字段是否正确复制
		nameField, exists := targetClass.Fields["name"]
		assert.True(t, exists, "name字段应该存在")
		assert.Equal(t, "string", nameField.Type, "字段类型应该相同")
		assert.Equal(t, "name", nameField.Name, "字段名应该相同")
		assert.Equal(t, "姓名字段", nameField.Description, "字段描述应该相同")
		assert.True(t, nameField.Nullable, "可空属性应该相同")
		assert.False(t, nameField.IsPrimary, "主键属性应该相同")

		idField, exists := targetClass.Fields["id"]
		assert.True(t, exists, "id字段应该存在")
		assert.Equal(t, "int", idField.Type, "字段类型应该相同")
		assert.Equal(t, "id", idField.Name, "字段名应该相同")
		assert.Equal(t, "ID字段", idField.Description, "字段描述应该相同")
		assert.False(t, idField.Nullable, "可空属性应该相同")
		assert.True(t, idField.IsPrimary, "主键属性应该相同")

		// 验证内存地址不同，确保是深复制
		assert.NotSame(t, sourceClass.Fields["name"], targetClass.Fields["name"], "应该是不同的对象实例(深复制)")
		assert.NotSame(t, sourceClass.Fields["id"], targetClass.Fields["id"], "应该是不同的对象实例(深复制)")
	})

	t.Run("关系字段复制测试", func(t *testing.T) {
		// 准备测试数据 - 带有关系的字段
		sourceClass := &internal.Class{
			Name:   "User",
			Table:  "users",
			Fields: make(map[string]*internal.Field),
		}

		// 添加带关系的字段
		userField := &internal.Field{
			Type:        "int",
			Name:        "departmentId",
			Column:      "department_id",
			Description: "部门ID",
			Nullable:    true,
			Relation: &internal.Relation{
				SourceClass: "User",
				SourceField: "departmentId",
				TargetClass: "Department",
				TargetField: "id",
				Type:        internal.MANY_TO_ONE,
			},
		}
		sourceClass.AddField(userField)

		targetClass := &internal.Class{
			Name:   "UserProfile",
			Table:  "user_profiles",
			Fields: make(map[string]*internal.Field),
		}

		// 执行复制操作
		meta.copyClassFields(targetClass, sourceClass)

		// 断言结果
		assert.Equal(t, 2, len(targetClass.Fields), "目标类应该有两个条目（1个主字段+1个列名索引）")

		// 验证字段是否正确复制
		deptField, exists := targetClass.Fields["departmentId"]
		assert.True(t, exists, "departmentId字段应该存在")
		assert.Equal(t, "int", deptField.Type, "字段类型应该相同")
		assert.Equal(t, "department_id", deptField.Column, "列名应该相同")
		assert.Equal(t, "部门ID", deptField.Description, "字段描述应该相同")

		// 验证关系是否正确复制和更新
		assert.NotNil(t, deptField.Relation, "关系应该被复制")
		assert.Equal(t, "UserProfile", deptField.Relation.SourceClass, "源类应该已更新为目标类名")
		assert.Equal(t, "departmentId", deptField.Relation.SourceField, "源字段应该保持不变")
		assert.Equal(t, "Department", deptField.Relation.TargetClass, "目标类应该保持不变")
		assert.Equal(t, "id", deptField.Relation.TargetField, "目标字段应该保持不变")
		assert.Equal(t, internal.MANY_TO_ONE, deptField.Relation.Type, "关系类型应该保持不变")

		// 验证内存地址不同，确保是深复制
		assert.NotSame(t, sourceClass.Fields["departmentId"], targetClass.Fields["departmentId"], "应该是不同的对象实例(深复制)")
		assert.NotSame(t, sourceClass.Fields["departmentId"].Relation, targetClass.Fields["departmentId"].Relation, "关系也应该是不同的对象实例(深复制)")

		// 验证列名索引是否正确
		colField, exists := targetClass.Fields["department_id"]
		assert.True(t, exists, "department_id索引应该存在")
		assert.Same(t, deptField, colField, "列名索引应该指向同一个对象")
	})

	t.Run("复杂嵌套关系复制测试", func(t *testing.T) {
		// 准备测试数据 - 带有Through关系的字段
		sourceClass := &internal.Class{
			Name:   "Post",
			Table:  "posts",
			Fields: make(map[string]*internal.Field),
		}

		// 添加多对多关系字段
		tagField := &internal.Field{
			Type:        "int",
			Name:        "tagId",
			Column:      "tag_id",
			Description: "标签ID",
			Nullable:    true,
			Relation: &internal.Relation{
				SourceClass: "Post",
				SourceField: "tagId",
				TargetClass: "Tag",
				TargetField: "id",
				Type:        internal.MANY_TO_MANY,
				Through: &internal.Through{
					Name:      "PostTag",
					Table:     "post_tags",
					SourceKey: "post_id",
					TargetKey: "tag_id",
					Fields: map[string]*internal.Field{
						"createdAt": {
							Type:        "timestamp",
							Name:        "createdAt",
							Column:      "created_at",
							Description: "创建时间",
							Nullable:    false,
						},
					},
				},
			},
		}
		sourceClass.AddField(tagField)

		targetClass := &internal.Class{
			Name:   "Article",
			Table:  "articles",
			Fields: make(map[string]*internal.Field),
		}

		// 执行复制操作
		meta.copyClassFields(targetClass, sourceClass)

		// 断言结果
		assert.Equal(t, 2, len(targetClass.Fields), "目标类应该有两个条目（1个主字段+1个列名索引）")

		// 验证字段是否正确复制
		tagIdField, exists := targetClass.Fields["tagId"]
		assert.True(t, exists, "tagId字段应该存在")

		// 验证关系是否正确复制和更新
		assert.NotNil(t, tagIdField.Relation, "关系应该被复制")
		assert.Equal(t, "Article", tagIdField.Relation.SourceClass, "源类应该已更新为目标类名")

		// 验证Through关系是否正确复制
		assert.NotNil(t, tagIdField.Relation.Through, "Through关系应该被复制")
		assert.Equal(t, "PostTag", tagIdField.Relation.Through.Name, "Through名称应该保持不变")
		assert.Equal(t, "post_tags", tagIdField.Relation.Through.Table, "Through表名应该保持不变")
		assert.Equal(t, "post_id", tagIdField.Relation.Through.SourceKey, "Source键应该保持不变")
		assert.Equal(t, "tag_id", tagIdField.Relation.Through.TargetKey, "Target键应该保持不变")

		// 验证Through字段是否正确复制
		assert.NotNil(t, tagIdField.Relation.Through.Fields, "Through字段集应该被复制")
		assert.Equal(t, 1, len(tagIdField.Relation.Through.Fields), "Through应该有一个字段")

		createdAtField, fieldExists := tagIdField.Relation.Through.Fields["createdAt"]
		assert.True(t, fieldExists, "createdAt字段应该存在")
		assert.Equal(t, "timestamp", createdAtField.Type, "字段类型应该相同")
		assert.Equal(t, "created_at", createdAtField.Column, "列名应该相同")
		assert.Equal(t, "创建时间", createdAtField.Description, "字段描述应该相同")
		assert.False(t, createdAtField.Nullable, "可空属性应该相同")

		// 验证内存地址不同，确保是深复制
		assert.NotSame(t, sourceClass.Fields["tagId"].Relation.Through, targetClass.Fields["tagId"].Relation.Through, "Through也应该是不同的对象实例(深复制)")
		assert.NotSame(t, sourceClass.Fields["tagId"].Relation.Through.Fields["createdAt"], targetClass.Fields["tagId"].Relation.Through.Fields["createdAt"], "Through字段也应该是不同的对象实例(深复制)")

		// 验证列名索引是否正确
		colField, exists := targetClass.Fields["tag_id"]
		assert.True(t, exists, "tag_id索引应该存在")
		assert.Same(t, tagIdField, colField, "列名索引应该指向同一个对象")
	})

	t.Run("索引字段处理测试", func(t *testing.T) {
		// 准备测试数据 - 包含索引字段
		sourceClass := &internal.Class{
			Name:   "Product",
			Table:  "products",
			Fields: make(map[string]*internal.Field),
		}

		// 添加原始字段和索引字段
		nameField := &internal.Field{
			Type:        "string",
			Name:        "name",
			Column:      "product_name", // 列名与字段名不同
			Description: "产品名称",
			Nullable:    false,
		}
		sourceClass.AddField(nameField)
		// 这会产生一个额外的索引，列名product_name也指向同一个字段对象

		targetClass := &internal.Class{
			Name:   "ProductView",
			Table:  "product_views",
			Fields: make(map[string]*internal.Field),
		}

		// 执行复制操作
		meta.copyClassFields(targetClass, sourceClass)

		// 断言结果 - 应该有1个字段和1个索引
		assert.Equal(t, 2, len(targetClass.Fields), "目标类应该有两个条目(1个主字段+1个列名索引)")

		// 验证字段是否正确复制
		nameField, exists := targetClass.Fields["name"]
		assert.True(t, exists, "name字段应该存在")
		assert.Equal(t, "string", nameField.Type, "字段类型应该相同")
		assert.Equal(t, "product_name", nameField.Column, "列名应该相同")

		// 验证克隆后的AddField是否正确添加了列名索引
		columnField, exists := targetClass.Fields["product_name"]
		assert.True(t, exists, "列名索引应该存在")
		assert.Same(t, nameField, columnField, "列名索引应该指向同一个对象")
	})
}
