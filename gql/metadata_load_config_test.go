package gql

import (
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/protocol"
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

	meta, err := NewMetadata(k, nil, WithoutLoader(protocol.LoaderFile))
	require.NoError(t, err, "创建元数据加载器失败")

	t.Run("PublicUser别名类", func(t *testing.T) {
		class, exists := meta.Nodes["PublicUser"]
		require.True(t, exists, "应该存在PublicUser类")
		assert.Equal(t, "用户公开信息", class.Description)
		assert.Equal(t, "users", class.Table)
		// 字段继承与排除
		assertField(t, class, "id", "id", "int", true, false, false, "用户ID", "")
		assertField(t, class, "name", "name", "string", false, false, false, "用户昵称", "MaskedNameResolver")
		_, exists = class.Fields["email"]
		assert.False(t, exists, "email字段应该被排除")
		_, exists = class.Fields["created_at"]
		assert.False(t, exists, "created_at字段应该被排除")
		// 指针独立性
		assert.NotSame(t, class, meta.Nodes["users"])
	})

	t.Run("AdminUser覆盖类", func(t *testing.T) {
		class, exists := meta.Nodes["AdminUser"]
		require.True(t, exists, "应该存在AdminUser类")
		assert.Equal(t, "管理员视图", class.Description)
		assert.Equal(t, "users", class.Table)
		assertField(t, class, "role", "", "string", false, false, false, "角色", "RoleResolver")
		_, exists = class.Fields["name"]
		assert.True(t, exists, "name字段应该存在")
		_, exists = class.Fields["email"]
		assert.True(t, exists, "email字段应该存在")
		// AdminUser应覆盖所有users相关索引
		tablePtr, tableExists := meta.Nodes["users"]
		assert.True(t, tableExists)
		assert.Same(t, class, tablePtr, "AdminUser和users索引应指向同一实例")
		// User索引应不存在
		_, exists = meta.Nodes["User"]
		assert.False(t, exists, "User类索引应被覆盖")
	})

	t.Run("Statistics虚拟类", func(t *testing.T) {
		class, exists := meta.Nodes["Statistics"]
		require.True(t, exists, "应该存在Statistics类")
		assert.True(t, class.Virtual)
		assert.Equal(t, "", class.Table)
		assert.Equal(t, "统计数据", class.Description)
		assert.Equal(t, "StatisticsResolver", class.Resolver)
		assertField(t, class, "totalUsers", "", "integer", false, false, false, "用户总数", "CountUsersResolver")
		assertField(t, class, "activeUsers", "", "integer", false, false, false, "活跃用户数", "CountActiveUsersResolver")
	})

	t.Run("MiniUser包含字段类", func(t *testing.T) {
		class, exists := meta.Nodes["MiniUser"]
		require.True(t, exists, "应该存在MiniUser类")
		assert.Equal(t, "users", class.Table)
		assert.Equal(t, "用户简要信息", class.Description)
		assertField(t, class, "id", "id", "int", true, false, false, "用户ID", "")
		assertField(t, class, "name", "name", "string", false, false, false, "用户名", "")
		assertField(t, class, "displayName", "", "string", false, false, false, "显示名称", "DisplayNameResolver")
		_, exists = class.Fields["email"]
		assert.False(t, exists, "email字段应该被排除")
		_, exists = class.Fields["created_at"]
		assert.False(t, exists, "created_at字段应该被排除")
	})

	t.Run("多重索引一致性与边界", func(t *testing.T) {
		for classKey, class := range meta.Nodes {
			if classKey == class.Table {
				tablePtr, tableExists := meta.Nodes[class.Name]
				assert.True(t, tableExists)
				assert.Same(t, class, tablePtr)
			}
			for fieldKey, field := range class.Fields {
				if fieldKey == field.Column {
					colPtr, colExists := class.Fields[field.Name]
					assert.True(t, colExists)
					assert.Same(t, field, colPtr)
				}
			}
		}
		// 不存在的类/字段
		_, exists := meta.Nodes["NotExist"]
		assert.False(t, exists)
		if user, ok := meta.Nodes["AdminUser"]; ok {
			_, exists = user.Fields["notExistField"]
			assert.False(t, exists)
		}
	})
}

// assertField 辅助函数，断言字段多重索引和属性
func assertField(t *testing.T, class *internal.Class, name, column, fieldType string, isPrimary, isUnique, nullable bool, description, resolver string) {
	field, exists := class.Fields[name]
	require.True(t, exists, "字段 %s 不存在", name)
	assert.Equal(t, name, field.Name)
	assert.Equal(t, column, field.Column)
	assert.Equal(t, fieldType, field.Type)
	assert.Equal(t, isPrimary, field.IsPrimary)
	assert.Equal(t, isUnique, field.IsUnique)
	assert.Equal(t, nullable, field.Nullable)
	assert.Equal(t, description, field.Description)
	assert.Equal(t, resolver, field.Resolver)
	if column != "" {
		colPtr, colExists := class.Fields[column]
		assert.True(t, colExists, "列名索引 %s 应该存在", column)
		assert.Same(t, field, colPtr, "字段名 %s 和列名 %s 应该指向同一个Field实例", name, column)
	}
}
