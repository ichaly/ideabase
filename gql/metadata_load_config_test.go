package gql

import (
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/metadata"
	"github.com/ichaly/ideabase/std"
	"github.com/ichaly/ideabase/utl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetadataLoadFromConfig(t *testing.T) {
	k, err := std.NewKonfig()
	require.NoError(t, err, "创建配置失败")
	k.Set("mode", "test")
	k.Set("app.root", utl.Root())
	k.Set("metadata.classes", map[string]*internal.ClassConfig{
		// 1. 基本类定义
		"User": {
			Table:       "users",
			Description: "用户表",
			PrimaryKeys: []string{"id"},
			Fields: map[string]*internal.FieldConfig{
				"id": {
					Column:      "id",
					Type:        "int",
					Description: "用户ID",
					IsPrimary:   true,
				},
				"name": {
					Column:      "name",
					Type:        "string",
					Description: "用户名",
				},
				"email": {
					Column:      "email",
					Type:        "string",
					Description: "邮箱",
				},
				"created_at": {
					Column:      "created_at",
					Type:        "time.Time",
					Description: "创建时间",
				},
			},
		},
		// 2. 别名类（追加模式）
		"PublicUser": {
			Table:         "users",
			Description:   "用户公开信息",
			ExcludeFields: []string{"email", "created_at"},
			Fields: map[string]*internal.FieldConfig{
				"name": {
					Description: "用户昵称",
					Resolver:    "MaskedNameResolver",
				},
			},
		},
		// 3. 别名类（覆盖模式）
		"AdminUser": {
			Table:       "users",
			Description: "管理员视图",
			Override:    true,
			Fields: map[string]*internal.FieldConfig{
				"role": {
					Column:      "role_id",
					Type:        "string",
					Description: "角色",
					Resolver:    "RoleResolver",
				},
			},
		},
		// 4. 虚拟类
		"Statistics": {
			Description: "统计数据",
			Resolver:    "StatisticsResolver",
			Fields: map[string]*internal.FieldConfig{
				"totalUsers": {
					Type:        "integer",
					Description: "用户总数",
					Resolver:    "CountUsersResolver",
				},
				"activeUsers": {
					Type:        "integer",
					Description: "活跃用户数",
					Resolver:    "CountActiveUsersResolver",
				},
			},
		},
		// 5. 使用包含字段的类
		"MiniUser": {
			Table:         "users",
			Description:   "用户简要信息",
			IncludeFields: []string{"id", "name"},
			Fields: map[string]*internal.FieldConfig{
				"displayName": {
					Type:        "string",
					Description: "显示名称",
					Resolver:    "DisplayNameResolver",
				},
			},
		},
	})

	meta, err := NewMetadata(k, nil, WithoutLoader(metadata.LoaderFile))
	require.NoError(t, err, "创建元数据加载器失败")

	// 1. 测试基本类定义
	t.Run("基本类定义", func(t *testing.T) {
		user, exists := meta.Nodes["User"]
		require.True(t, exists, "应该存在User类")
		assert.Equal(t, "用户表", user.Description, "类描述应该正确")
		assert.Equal(t, "users", user.Table, "表名应该正确")
		assert.Equal(t, []string{"id"}, user.PrimaryKeys, "主键应该正确")

		// 验证字段
		assertField(t, user, "id", "id", "int", true, false, false, "用户ID", "")
		assertField(t, user, "name", "name", "string", false, false, false, "用户名", "")
		assertField(t, user, "email", "email", "string", false, false, false, "邮箱", "")
		assertField(t, user, "created_at", "created_at", "time.Time", false, false, false, "创建时间", "")
	})

	// 2. 测试别名类（追加模式）
	t.Run("别名类-追加模式", func(t *testing.T) {
		class, exists := meta.Nodes["PublicUser"]
		require.True(t, exists, "应该存在PublicUser类")
		assert.Equal(t, "用户公开信息", class.Description, "类描述应该正确")
		assert.Equal(t, "users", class.Table, "表名应该正确")

		// 验证字段（继承自基类但排除了敏感字段）
		assertField(t, class, "id", "id", "int", true, false, false, "用户ID", "")
		assertField(t, class, "name", "name", "string", false, false, false, "用户昵称", "MaskedNameResolver")

		// 验证敏感字段已被排除
		_, exists = class.Fields["email"]
		assert.False(t, exists, "email字段应该被排除")
		_, exists = class.Fields["created_at"]
		assert.False(t, exists, "created_at字段应该被排除")
	})

	// 3. 测试别名类（覆盖模式）
	t.Run("别名类-覆盖模式", func(t *testing.T) {
		class, exists := meta.Nodes["AdminUser"]
		require.True(t, exists, "应该存在AdminUser类")
		assert.Equal(t, "管理员视图", class.Description, "类描述应该正确")
		assert.Equal(t, "users", class.Table, "表名应该正确")

		// 验证字段（完全覆盖基类）
		assertField(t, class, "role", "role_id", "string", false, false, false, "角色", "RoleResolver")

		// 验证基类字段已被覆盖
		_, exists = class.Fields["name"]
		assert.False(t, exists, "name字段应该被覆盖")
		_, exists = class.Fields["email"]
		assert.False(t, exists, "email字段应该被覆盖")
	})

	// 4. 测试虚拟类
	t.Run("虚拟类", func(t *testing.T) {
		class, exists := meta.Nodes["Statistics"]
		require.True(t, exists, "应该存在Statistics类")
		assert.True(t, class.Virtual, "应该是虚拟类")
		assert.Equal(t, "", class.Table, "虚拟类不应该有表名")
		assert.Equal(t, "统计数据", class.Description, "类描述应该正确")
		assert.Equal(t, "StatisticsResolver", class.Resolver, "解析器应该正确")

		// 验证字段
		assertField(t, class, "totalUsers", "totalUsers", "integer", false, false, false, "用户总数", "CountUsersResolver")
		assertField(t, class, "activeUsers", "activeUsers", "integer", false, false, false, "活跃用户数", "CountActiveUsersResolver")
	})

	// 5. 测试包含字段的类
	t.Run("包含字段的类", func(t *testing.T) {
		class, exists := meta.Nodes["MiniUser"]
		require.True(t, exists, "应该存在MiniUser类")
		assert.Equal(t, "users", class.Table, "表名应该正确")
		assert.Equal(t, "用户简要信息", class.Description, "类描述应该正确")

		// 验证只包含指定字段
		assertField(t, class, "id", "id", "int", true, false, false, "用户ID", "")
		assertField(t, class, "name", "name", "string", false, false, false, "用户名", "")
		assertField(t, class, "displayName", "displayName", "string", false, false, false, "显示名称", "DisplayNameResolver")

		// 验证其他字段已被排除
		_, exists = class.Fields["email"]
		assert.False(t, exists, "email字段应该被排除")
		_, exists = class.Fields["created_at"]
		assert.False(t, exists, "created_at字段应该被排除")
	})

	// 6. 验证索引一致性
	t.Run("索引一致性", func(t *testing.T) {
		for _, class := range meta.Nodes {
			// 验证表名索引
			tablePtr, tableExists := meta.Nodes[class.Table]
			if tableExists && class.Table != "" {
				assert.Same(t, class, tablePtr, "类名 %s 和表名 %s 应该指向同一个Class实例", class.Name, class.Table)
			}

			// 验证别名索引
			for alias, node := range meta.Nodes {
				if alias != class.Name && alias != class.Table && node.Table == class.Table {
					assert.NotSame(t, class, node, "别名 %s 应该是新的Class指针", alias)
				}
			}

			// 验证字段索引
			for fieldName, field := range class.Fields {
				if field.Column != "" {
					colPtr, colExists := class.Fields[field.Column]
					if colExists {
						assert.Same(t, field, colPtr, "字段名 %s 和列名 %s 应该指向同一个Field实例", fieldName, field.Column)
					}
				}
			}
		}
	})
}

// assertField 辅助函数，用于验证字段属性
func assertField(t *testing.T, class *internal.Class, name, column, fieldType string, isPrimary, isUnique, nullable bool, description, resolver string) {
	field, exists := class.Fields[name]
	require.True(t, exists, "字段 %s 不存在", name)
	if !exists {
		return
	}
	assert.Equal(t, name, field.Name, "字段名应该正确")
	assert.Equal(t, column, field.Column, "列名应该正确")
	assert.Equal(t, fieldType, field.Type, "字段类型应该正确")
	assert.Equal(t, isPrimary, field.IsPrimary, "主键标志应该正确")
	assert.Equal(t, isUnique, field.IsUnique, "唯一标志应该正确")
	assert.Equal(t, nullable, field.Nullable, "可空标志应该正确")
	assert.Equal(t, description, field.Description, "字段描述应该正确")
	assert.Equal(t, resolver, field.Resolver, "解析器应该正确")
}
