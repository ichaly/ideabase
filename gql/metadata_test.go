package gql

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/utl"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func init() {
	// 加载.env文件
	if err := godotenv.Load("../.env"); err != nil {
		println("警告: 未能加载 .env 文件:", err)
	}
}

// setupTestDatabase 初始化测试数据库
func setupTestDatabase(t *testing.T) (*gorm.DB, func()) {
	ctx := context.Background()

	// 创建PostgreSQL容器请求
	req := testcontainers.ContainerRequest{
		Image:        "postgres:15-alpine",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_DB":       "test",
			"POSTGRES_USER":     "test",
			"POSTGRES_PASSWORD": "test",
		},
		WaitingFor: wait.ForLog("database system is ready to accept connections"),
	}

	// 启动容器
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err, "创建PostgreSQL容器失败")

	// 获取连接信息
	host, err := container.Host(ctx)
	require.NoError(t, err, "获取容器主机失败")
	port, err := container.MappedPort(ctx, "5432")
	require.NoError(t, err, "获取容器端口失败")

	// 等待数据库就绪
	time.Sleep(2 * time.Second)

	// 构建连接字符串
	dsn := fmt.Sprintf("host=%s port=%s user=test password=test dbname=test sslmode=disable", host, port.Port())

	// 连接数据库
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err, "连接数据库失败")

	// 创建测试表结构
	// 读取PostgreSQL建表SQL文件
	sqlBytes, err := os.ReadFile(filepath.Join(utl.Root(), "gql/assets/sql/pgsql.sql"))
	require.NoError(t, err, "读取SQL文件失败")

	// 执行建表SQL
	err = db.Exec(string(sqlBytes)).Error
	require.NoError(t, err, "创建测试表结构失败")

	// 返回清理函数
	cleanup := func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
		if err := container.Terminate(ctx); err != nil {
			t.Logf("终止容器失败: %v", err)
		}
	}

	return db, cleanup
}

// TestMetadataLoadingModes 测试三种元数据加载模式
func TestMetadataLoadingModes(t *testing.T) {
	// 初始化测试数据库
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	// 1. 测试从数据库加载
	t.Run("从数据库加载", func(t *testing.T) {
		v := viper.New()
		v.Set("mode", "dev")
		v.Set("app.root", utl.Root())
		v.Set("schema.schema", "public")
		v.Set("schema.enable-camel-case", true)

		meta, err := NewMetadata(v, db)
		require.NoError(t, err, "创建元数据加载器失败")
		assert.NotEmpty(t, meta.Nodes, "应该从数据库加载到元数据")

		// 验证表的加载
		tables := []string{"Users", "Posts", "Tags", "PostTags"}
		for _, tableName := range tables {
			class, exists := meta.Nodes[tableName]
			assert.True(t, exists, "应该存在表 %s", tableName)
			if exists {
				assert.NotEmpty(t, class.Fields, "表 %s 应该有字段", tableName)
			}
		}

		// 验证关系加载
		posts, exists := meta.Nodes["Posts"]
		assert.True(t, exists, "应该存在Posts表")
		if exists {
			// 验证many-to-one关系
			userId := posts.Fields["userId"]
			assert.NotNil(t, userId, "应该有userId字段")
			if userId != nil {
				assert.NotNil(t, userId.Relation, "userId应该有关系定义")
				assert.Equal(t, internal.MANY_TO_ONE, userId.Relation.Type, "应该是many-to-one关系")
				assert.Equal(t, "Users", userId.Relation.TargetClass, "关系目标类应该是Users")
			}
		}

		// 验证many-to-many关系
		postTags, exists := meta.Nodes["PostTags"]
		assert.True(t, exists, "应该存在PostTags表")
		if exists {
			// 验证与Posts的关系
			postId := postTags.Fields["postId"]
			assert.NotNil(t, postId, "应该有postId字段")
			if postId != nil {
				assert.NotNil(t, postId.Relation, "postId应该有关系定义")
				assert.Equal(t, internal.MANY_TO_ONE, postId.Relation.Type, "应该是many-to-one关系")
				assert.Equal(t, "Posts", postId.Relation.TargetClass, "关系目标类应该是Posts")
			}

			// 验证与Tags的关系
			tagId := postTags.Fields["tagId"]
			assert.NotNil(t, tagId, "应该有tagId字段")
			if tagId != nil {
				assert.NotNil(t, tagId.Relation, "tagId应该有关系定义")
				assert.Equal(t, internal.MANY_TO_ONE, tagId.Relation.Type, "应该是many-to-one关系")
				assert.Equal(t, "Tags", tagId.Relation.TargetClass, "关系目标类应该是Tags")
			}
		}

		// 验证自关联关系
		organizations, exists := meta.Nodes["Organizations"]
		assert.True(t, exists, "应该存在Organizations表")
		if exists {
			// 验证parentId字段
			parentId := organizations.Fields["parentId"]
			assert.NotNil(t, parentId, "应该有parentId字段")
			if parentId != nil {
				assert.NotNil(t, parentId.Relation, "parentId应该有关系定义")
				assert.Equal(t, internal.RECURSIVE, parentId.Relation.Type, "应该是recursive关系")
				assert.Equal(t, "Organizations", parentId.Relation.TargetClass, "关系目标类应该是Organizations")
				assert.Equal(t, "id", parentId.Relation.TargetField, "关系目标字段应该是id")

				// 验证反向关系
				assert.NotNil(t, parentId.Relation.Reverse, "应该有反向关系")
				if parentId.Relation.Reverse != nil {
					assert.Equal(t, internal.RECURSIVE, parentId.Relation.Reverse.Type, "反向关系也应该是recursive")
					assert.Equal(t, "Organizations", parentId.Relation.Reverse.TargetClass, "反向关系目标类应该是Organizations")
				}
			}
		}
	})

	// 2. 测试从文件加载
	t.Run("从文件加载", func(t *testing.T) {
		// 先从数据库加载并保存到test.json
		v := viper.New()
		v.Set("mode", "dev")

		v.Set("app.root", "../")
		loader1, err := NewMetadata(v, db)
		require.NoError(t, err, "从数据库创建元数据加载器失败")
		err = loader1.saveToFile("../cfg/metadata.test.json")
		require.NoError(t, err, "保存元数据到文件失败")

		// 从test.json加载
		v.Set("mode", "test")
		loader2, err := NewMetadata(v, nil)
		require.NoError(t, err, "从文件创建元数据加载器失败")
		require.NotEmpty(t, loader2.Nodes, "元数据不应为空")

		// 验证两个加载器的内部构成是否一致
		assert.Equal(t, len(loader1.Nodes), len(loader2.Nodes), "节点数量应该相同")
		assert.Equal(t, loader1.Version, loader2.Version, "版本应该相同")

		// 遍历所有节点进行深度比较
		for name, class1 := range loader1.Nodes {
			class2, exists := loader2.Nodes[name]
			assert.True(t, exists, "节点 %s 应该存在于两个加载器中", name)
			if exists {
				// 比较类的基本属性
				assert.Equal(t, class1.Name, class2.Name, "类名应该相同")
				assert.Equal(t, class1.Table, class2.Table, "表名应该相同")
				assert.Equal(t, class1.Virtual, class2.Virtual, "虚拟属性应该相同")
				assert.Equal(t, class1.Description, class2.Description, "描述应该相同")
				assert.Equal(t, class1.PrimaryKeys, class2.PrimaryKeys, "主键应该相同")

				// 比较字段
				assert.Equal(t, len(class1.Fields), len(class2.Fields), "字段数量应该相同")
				for fieldName, field1 := range class1.Fields {
					field2, fieldExists := class2.Fields[fieldName]
					assert.True(t, fieldExists, "字段 %s 应该存在于两个类中", fieldName)
					if fieldExists {
						// 比较字段属性
						assert.Equal(t, field1.Name, field2.Name, "字段名应该相同")
						assert.Equal(t, field1.Type, field2.Type, "字段类型应该相同")
						assert.Equal(t, field1.Column, field2.Column, "列名应该相同")
						assert.Equal(t, field1.Virtual, field2.Virtual, "虚拟属性应该相同")
						assert.Equal(t, field1.Nullable, field2.Nullable, "可空属性应该相同")
						assert.Equal(t, field1.IsPrimary, field2.IsPrimary, "主键属性应该相同")
						assert.Equal(t, field1.IsUnique, field2.IsUnique, "唯一属性应该相同")
						assert.Equal(t, field1.Description, field2.Description, "描述应该相同")

						// 比较关系定义
						if field1.Relation != nil {
							assert.NotNil(t, field2.Relation, "关系定义应该同时存在或不存在")
							if field2.Relation != nil {
								assert.Equal(t, field1.Relation.Type, field2.Relation.Type, "关系类型应该相同")
								assert.Equal(t, field1.Relation.TargetClass, field2.Relation.TargetClass, "目标类应该相同")
								assert.Equal(t, field1.Relation.TargetField, field2.Relation.TargetField, "目标字段应该相同")
							}
						} else {
							assert.Nil(t, field2.Relation, "关系定义应该同时存在或不存在")
						}
					}
				}
			}
		}
	})

	// 3. 测试配置增强
	t.Run("配置增强", func(t *testing.T) {
		// 创建配置
		v := viper.New()
		v.Set("mode", "test")
		v.Set("app.root", "../")
		v.Set("metadata.classes", map[string]*internal.ClassConfig{
			"User": {
				Table:       "users",
				Description: "用户表",
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
				},
			},
			"VirtualTable": {
				Description: "虚拟表",
				Fields: map[string]*internal.FieldConfig{
					"id": {
						Column:    "id",
						Type:      "int",
						IsPrimary: true,
					},
				},
			},
		})

		// 创建元数据加载器
		loader, err := NewMetadata(v, nil)
		require.NoError(t, err, "创建元数据加载器失败")
		err = loader.loadMetadata()
		require.NoError(t, err, "加载元数据失败")

		// 验证元数据
		user, exists := loader.Nodes["User"]
		require.True(t, exists, "应该存在User类")
		require.Equal(t, "用户表", user.Description, "类描述应该正确")
		require.Equal(t, "users", user.Table, "表名应该正确")

		id, exists := user.Fields["id"]
		require.True(t, exists, "应该存在id字段")
		require.Equal(t, "用户ID", id.Description, "字段描述应该正确")

		name, exists := user.Fields["name"]
		require.True(t, exists, "应该存在name字段")
		require.Equal(t, "用户名", name.Description, "字段描述应该正确")

		// 验证表名索引
		userByTable, exists := loader.Nodes["users"]
		require.True(t, exists, "应该能通过表名找到类")
		require.Same(t, user, userByTable, "通过类名和表名应该找到同一个实例")

		vt, exists := loader.Nodes["VirtualTable"]
		require.True(t, exists, "应该存在VirtualTable类")
		require.Equal(t, "虚拟表", vt.Description, "虚拟表描述应该正确")
		require.True(t, vt.Virtual, "VirtualTable应该是虚拟类")
	})
}

// 测试数据库加载
func TestLoadMetadataFromDatabase(t *testing.T) {
	// 创建测试数据库
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	// 创建配置
	v := viper.New()
	v.Set("mode", "dev")
	v.Set("app.root", utl.Root())
	v.Set("schema.schema", "public")
	v.Set("schema.enable-camel-case", true)

	// 创建元数据加载器
	meta, err := NewMetadata(v, db)
	require.NoError(t, err, "创建元数据加载器失败")

	// 验证元数据已加载
	assert.NotEmpty(t, meta.Nodes, "应该有加载的类")
}

// 测试配置加载
func TestLoadMetadataFromConfig(t *testing.T) {
	// 创建测试数据库
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	// 创建临时配置文件
	configFile := filepath.Join(utl.Root(), "cfg", "metadata.config.json")
	configData := map[string]interface{}{
		"nodes": map[string]interface{}{
			"User": map[string]interface{}{
				"name":        "User",
				"table":       "users",
				"description": "用户表",
				"fields": map[string]interface{}{
					"username": map[string]interface{}{
						"name":        "username",
						"column":      "user_name",
						"type":        "character varying",
						"description": "用户名",
					},
					"password": map[string]interface{}{
						"name":        "password",
						"column":      "password",
						"type":        "character varying",
						"description": "密码",
					},
				},
			},
		},
	}
	configBytes, err := json.Marshal(configData)
	require.NoError(t, err, "序列化配置数据失败")
	err = os.MkdirAll(filepath.Dir(configFile), 0755)
	require.NoError(t, err, "创建配置目录失败")
	err = os.WriteFile(configFile, configBytes, 0644)
	require.NoError(t, err, "写入配置文件失败")
	defer os.Remove(configFile)

	// 创建配置
	v := viper.New()
	v.Set("mode", "config")
	v.Set("app.root", utl.Root())
	v.Set("metadata.file", "metadata.config.json") // 设置元数据配置文件路径

	// 创建元数据加载器
	meta, err := NewMetadata(v, db)
	require.NoError(t, err, "创建元数据加载器失败")

	// 验证元数据已加载
	assert.Len(t, meta.Nodes, 2, "应该有2个Node索引")

	// 通过类名查找
	userNode, ok := meta.Nodes["User"]
	assert.True(t, ok, "应该能通过类名找到Node")
	assert.Equal(t, "users", userNode.Table, "表名应该正确")
	assert.Len(t, userNode.Fields, 4, "应该有4个字段索引(2个字段名 + 2个列名)")

	// 验证字段
	username, ok := userNode.Fields["username"]
	assert.True(t, ok, "应该能找到username字段")
	assert.Equal(t, "user_name", username.Column, "列名应该正确")
	assert.Equal(t, "用户名", username.Description, "描述应该正确")

	// 验证列名索引
	userNameCol, ok := userNode.Fields["user_name"]
	assert.True(t, ok, "应该能通过列名找到字段")
	assert.Same(t, username, userNameCol, "通过字段名和列名应该找到同一个字段")

	// 通过表名查找
	tableNode, ok := meta.Nodes["users"]
	assert.True(t, ok, "应该能通过表名找到Node")
	assert.Same(t, userNode, tableNode, "通过类名和表名找到的应该是同一个Node")
}

// 测试名称转换
func TestNameConversion(t *testing.T) {
	// 创建测试数据库
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	// 创建配置
	v := viper.New()
	v.Set("mode", "dev")
	v.Set("app.root", utl.Root())
	v.Set("schema.enable-camel-case", true)
	v.Set("schema.table-prefix", []string{"tbl_"})

	// 设置测试元数据配置
	v.Set("metadata.classes", map[string]map[string]interface{}{
		"UserProfiles": {
			"table": "tbl_user_profiles",
			"fields": map[string]map[string]interface{}{
				"userId": {
					"column": "user_id",
					"type":   "integer",
				},
				"firstName": {
					"column": "first_name",
					"type":   "character varying",
				},
			},
		},
	})

	// 创建元数据加载器
	meta, err := NewMetadata(v, db)
	require.NoError(t, err, "创建元数据加载器失败")

	// 验证名称转换
	userProfilesNode, ok := meta.Nodes["UserProfiles"]
	assert.True(t, ok, "应该能通过类名找到Node")
	assert.Contains(t, userProfilesNode.Fields, "userId", "应该包含驼峰命名的字段")
	assert.Contains(t, userProfilesNode.Fields, "firstName", "应该包含驼峰命名的字段")

	// 验证列名索引
	assert.Contains(t, userProfilesNode.Fields, "user_id", "应该包含原始列名索引")
	assert.Contains(t, userProfilesNode.Fields, "first_name", "应该包含原始列名索引")

	// 验证原始表名索引
	origTableNode, ok := meta.Nodes["tbl_user_profiles"]
	assert.True(t, ok, "应该能通过原始表名找到Node")
	assert.Same(t, userProfilesNode, origTableNode, "应该是同一个Node")
}

// 测试表和字段过滤
func TestTableAndFieldFiltering(t *testing.T) {
	// 创建测试数据库
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	// 创建配置
	v := viper.New()
	v.Set("mode", "dev")
	v.Set("app.root", utl.Root())
	v.Set("schema.include-tables", []string{"users"})
	v.Set("schema.exclude-fields", []string{"password"})

	// 设置测试元数据配置
	v.Set("metadata.classes", map[string]map[string]interface{}{
		"User": {
			"table": "users",
			"fields": map[string]map[string]interface{}{
				"id": {
					"column": "id",
					"type":   "integer",
				},
				"name": {
					"column": "name",
					"type":   "character varying",
				},
				"password": {
					"column": "password",
					"type":   "character varying",
				},
			},
		},
		"Post": {
			"table": "posts",
			"fields": map[string]map[string]interface{}{
				"id": {
					"column": "id",
					"type":   "integer",
				},
			},
		},
	})

	// 创建元数据加载器
	meta, err := NewMetadata(v, db)
	require.NoError(t, err, "创建元数据加载器失败")

	// 验证表过滤
	assert.Len(t, meta.Nodes, 2, "应该只有users表的两个索引")
	_, ok := meta.Nodes["Post"]
	assert.False(t, ok, "Post类应该被过滤掉")
	_, ok = meta.Nodes["posts"]
	assert.False(t, ok, "posts表应该被过滤掉")

	// 验证字段过滤
	userNode, ok := meta.Nodes["User"]
	assert.True(t, ok, "应该能找到User类")
	assert.NotContains(t, userNode.Fields, "password", "password字段应该被过滤掉")
	assert.Contains(t, userNode.Fields, "name", "name字段应该保留")
}

// 测试从文件加载元数据
func TestLoadMetadataFromFile(t *testing.T) {
	// 创建测试数据库
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	// 创建配置
	v := viper.New()
	v.Set("mode", "dev")
	v.Set("app.root", utl.Root())

	// 创建元数据加载器
	meta, err := NewMetadata(v, db)
	require.NoError(t, err, "创建元数据加载器失败")

	// 验证基本信息
	assert.NotEmpty(t, meta.Version, "版本号不应为空")
	assert.Len(t, meta.Version, 14, "版本号应该是14位时间戳")

	// 检查是否有加载的节点
	assert.NotEmpty(t, meta.Nodes, "应该有加载的节点")

	// 检查是否存在 Users 节点
	users, ok := meta.Nodes["Users"]
	if ok {
		assert.Equal(t, "users", users.Table, "表名应该是users")
		assert.NotEmpty(t, users.Fields, "应该有字段")
	} else {
		// 如果没有 Users 节点，检查是否存在 users 节点
		users, ok = meta.Nodes["users"]
		assert.True(t, ok, "应该能找到users表")
		assert.NotEmpty(t, users.Fields, "应该有字段")
	}
}

// 测试关系名称转换
func TestRelationNameConversion(t *testing.T) {
	// 创建测试数据库
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	// 创建配置
	v := viper.New()
	v.Set("mode", "dev")
	v.Set("app.root", utl.Root())
	v.Set("schema.schema", "public")
	v.Set("schema.enable-camel-case", true)

	// 设置外键关系配置
	v.Set("metadata.classes", map[string]map[string]interface{}{
		"UserProfiles": {
			"table": "user_profiles",
			"fields": map[string]map[string]interface{}{
				"userId": {
					"column": "user_id",
					"type":   "integer",
					"relation": map[string]interface{}{
						"type":         "many_to_one",
						"target_class": "Users",
						"target_field": "id",
					},
				},
			},
		},
	})

	// 创建元数据加载器
	meta, err := NewMetadata(v, db)
	require.NoError(t, err, "创建元数据加载器失败")

	// 验证关系名称转换
	t.Run("验证关系名称转换", func(t *testing.T) {
		// 获取转换后的类和字段
		userProfiles, ok := meta.Nodes["UserProfiles"]
		require.True(t, ok, "应该能找到UserProfiles")

		// 验证字段名转换
		userId, ok := userProfiles.Fields["userId"]
		require.True(t, ok, "应该有userId字段")
		require.NotNil(t, userId.Relation, "应该有关系定义")

		// 验证关系中的名称是否转换正确
		assert.Equal(t, "UserProfiles", userId.Relation.SourceClass, "源类名应该是转换后的UserProfiles")
		assert.Equal(t, "userId", userId.Relation.SourceField, "源字段名应该是转换后的userId")
		assert.Equal(t, "Users", userId.Relation.TargetClass, "目标类名应该是转换后的Users")
		assert.Equal(t, "id", userId.Relation.TargetField, "目标字段名应该是id")
	})

	// 验证多对多关系名称转换
	t.Run("验证多对多关系名称转换", func(t *testing.T) {
		// 检查post_tags中间表中的关系
		postTags, ok := meta.Nodes["PostTags"]
		require.True(t, ok, "应该能找到PostTags")

		// 验证post_id字段关系
		postId, ok := postTags.Fields["postId"]
		require.True(t, ok, "应该有postId字段")
		require.NotNil(t, postId.Relation, "应该有关系定义")

		// 验证关系中的名称是否转换正确
		assert.Equal(t, "PostTags", postId.Relation.SourceClass, "源类名应该是转换后的PostTags")
		assert.Equal(t, "postId", postId.Relation.SourceField, "源字段名应该是转换后的postId")
		assert.Equal(t, "Posts", postId.Relation.TargetClass, "目标类名应该是转换后的Posts")
		assert.Equal(t, "id", postId.Relation.TargetField, "目标字段名应该是id")

		// 验证自动创建的多对多关系
		posts, ok := meta.Nodes["Posts"]
		require.True(t, ok, "应该能找到Posts")

		// 检查从Posts到Tags的多对多关系字段
		// 不硬编码字段名，而是查找一个从Posts到Tags的关系
		foundTagsRelation := false
		for fieldName, field := range posts.Fields {
			if field.Relation != nil && field.Relation.TargetClass == "Tags" {
				foundTagsRelation = true
				t.Logf("找到从Posts到Tags的关系字段: %s", fieldName)
				assert.Equal(t, "Posts", field.Relation.SourceClass, "源类名应该是转换后的Posts")
				assert.Equal(t, "id", field.Relation.SourceField, "源字段名应该保持为id")
				assert.Equal(t, "Tags", field.Relation.TargetClass, "目标类名应该是转换后的Tags")
				assert.Equal(t, "id", field.Relation.TargetField, "目标字段名应该保持为id")
				break
			}
		}
		assert.True(t, foundTagsRelation, "应该有从Posts到Tags的关系字段")
	})
}

// TestNewMetadataFeatures 测试元数据配置系统的新功能
func TestNewMetadataFeatures(t *testing.T) {
	// 创建测试数据库
	db, cleanup := setupTestDatabase(t)
	defer cleanup()

	// 创建配置
	v := viper.New()
	v.Set("mode", "dev")
	v.Set("app.root", utl.Root())
	v.Set("schema.schema", "public")
	v.Set("schema.enable-camel-case", true)

	// 设置元数据配置
	v.Set("metadata.classes", map[string]map[string]interface{}{
		// 1. 完整用户视图（管理员使用）
		"User": {
			"table":       "users",
			"description": "用户完整信息",
			"resolver":    "UserResolver",
			"fields": map[string]map[string]interface{}{
				"email": {
					"description": "电子邮箱",
					"resolver":    "EmailResolver",
				},
				"password": {
					"type":        "string",
					"description": "密码（加密存储）",
				},
				"fullName": {
					"virtual":     true,
					"type":        "string",
					"description": "用户全名",
					"resolver":    "FullNameResolver",
				},
			},
		},
		// 2. 公开用户视图（去除敏感信息）
		"PublicUser": {
			"table":          "users",
			"description":    "用户公开信息",
			"exclude_fields": []string{"password", "createdAt"},
			"fields": map[string]map[string]interface{}{
				"email": {
					"description": "电子邮箱(脱敏)",
					"resolver":    "MaskedEmailResolver",
				},
			},
		},
		// 3. 简要用户视图（使用include_fields）
		"MiniUser": {
			"table":          "users",
			"description":    "用户简要信息",
			"include_fields": []string{"id", "name"},
		},
		// 4. 虚拟表配置
		"Statistics": {
			"virtual":     true,
			"description": "统计数据",
			"resolver":    "StatisticsResolver",
			"fields": map[string]map[string]interface{}{
				"totalUsers": {
					"virtual":     true,
					"type":        "integer",
					"description": "用户总数",
					"resolver":    "CountUsersResolver",
				},
				"activeUsers": {
					"virtual":     true,
					"type":        "integer",
					"description": "活跃用户数",
					"resolver":    "CountActiveUsersResolver",
				},
			},
		},
		// 5. 带中间表配置的多对多关系
		"Post": {
			"table": "posts",
			"fields": map[string]map[string]interface{}{
				"tags": {
					"virtual": true,
					"relation": map[string]interface{}{
						"type":         "many_to_many",
						"target_class": "Tag",
						"target_field": "posts",
						"through": map[string]interface{}{
							"table":      "post_tags",
							"source_key": "post_id",
							"target_key": "tag_id",
							"class_name": "PostTag",
							"fields": map[string]map[string]interface{}{
								"createdAt": {
									"column":      "created_at",
									"type":        "timestamp",
									"description": "标签添加时间",
								},
							},
						},
					},
				},
			},
		},
		"Tag": {
			"table": "tags",
			"fields": map[string]map[string]interface{}{
				"posts": {
					"virtual": true,
					"relation": map[string]interface{}{
						"type":         "many_to_many",
						"target_class": "Post",
						"target_field": "tags",
						"through": map[string]interface{}{
							"table":      "post_tags",
							"source_key": "tag_id",
							"target_key": "post_id",
							"class_name": "PostTag",
						},
					},
				},
			},
		},
	})

	// 创建元数据加载器
	meta, err := NewMetadata(v, db)
	require.NoError(t, err, "创建元数据加载器失败")

	// 1. 测试同表不同视图
	t.Run("同表不同视图", func(t *testing.T) {
		// 验证完整用户视图
		user, exists := meta.Nodes["User"]
		assert.True(t, exists, "应该存在User类")
		assert.Equal(t, "users", user.Table, "表名应该是users")
		assert.Equal(t, "UserResolver", user.Resolver, "应该有类级别Resolver")

		passwordField := user.Fields["password"]
		assert.NotNil(t, passwordField, "应该存在password字段")
		assert.Equal(t, "密码（加密存储）", passwordField.Description, "描述应该正确")

		emailField := user.Fields["email"]
		assert.NotNil(t, emailField, "应该存在email字段")
		assert.Equal(t, "EmailResolver", emailField.Resolver, "应该有字段级别Resolver")

		// 验证公开用户视图
		publicUser, exists := meta.Nodes["PublicUser"]
		assert.True(t, exists, "应该存在PublicUser类")
		assert.Equal(t, "users", publicUser.Table, "表名应该是users")

		pubPasswordField := publicUser.Fields["password"]
		assert.Nil(t, pubPasswordField, "不应该存在password字段")

		pubEmailField := publicUser.Fields["email"]
		assert.NotNil(t, pubEmailField, "应该存在email字段")
		assert.Equal(t, "电子邮箱(脱敏)", pubEmailField.Description, "描述应该被覆盖")
		assert.Equal(t, "MaskedEmailResolver", pubEmailField.Resolver, "应该有字段级别Resolver")

		// 验证简要用户视图
		miniUser, exists := meta.Nodes["MiniUser"]
		assert.True(t, exists, "应该存在MiniUser类")
		assert.Equal(t, "users", miniUser.Table, "表名应该是users")

		// 打印所有可用的字段
		t.Logf("MiniUser类的所有字段：")
		for fieldName := range miniUser.Fields {
			t.Logf(" - %s", fieldName)
		}

		// 验证包含的字段
		assert.NotNil(t, miniUser.Fields["id"], "应该存在id字段")
		assert.NotNil(t, miniUser.Fields["name"], "应该存在name字段")
		assert.Nil(t, miniUser.Fields["email"], "不应该存在email字段")
		assert.Nil(t, miniUser.Fields["password"], "不应该存在password字段")
	})

	// 2. 测试虚拟表
	t.Run("虚拟表", func(t *testing.T) {
		stats, exists := meta.Nodes["Statistics"]
		assert.True(t, exists, "应该存在Statistics类")
		assert.True(t, stats.Virtual, "Statistics应该是虚拟类")
		assert.Equal(t, "StatisticsResolver", stats.Resolver, "应该有类级别Resolver")

		totalUsers := stats.Fields["totalUsers"]
		assert.NotNil(t, totalUsers, "应该存在totalUsers字段")
		assert.True(t, totalUsers.Virtual, "totalUsers应该是虚拟字段")
		assert.Equal(t, "CountUsersResolver", totalUsers.Resolver, "应该有字段级别Resolver")

		activeUsers := stats.Fields["activeUsers"]
		assert.NotNil(t, activeUsers, "应该存在activeUsers字段")
		assert.True(t, activeUsers.Virtual, "activeUsers应该是虚拟字段")
		assert.Equal(t, "CountActiveUsersResolver", activeUsers.Resolver, "应该有字段级别Resolver")
	})

	// 3. 测试多对多关系增强
	t.Run("多对多关系增强", func(t *testing.T) {
		post, exists := meta.Nodes["Post"]
		assert.True(t, exists, "应该存在Post类")

		tags := post.Fields["tags"]
		assert.NotNil(t, tags, "应该存在tags字段")
		assert.NotNil(t, tags.Relation, "tags应该有关系定义")
		assert.Equal(t, internal.MANY_TO_MANY, tags.Relation.Type, "应该是多对多关系")
		assert.Equal(t, "Tag", tags.Relation.TargetClass, "关系目标类应该是Tag")
		assert.Equal(t, "posts", tags.Relation.TargetField, "关系目标字段应该是posts")

		// 验证中间表配置
		assert.NotNil(t, tags.Relation.Through, "应该有through配置")
		assert.Equal(t, "post_tags", tags.Relation.Through.Table, "中间表名应该是post_tags")
		assert.Equal(t, "PostTag", tags.Relation.Through.Name, "中间表类名应该是PostTag")
		assert.Equal(t, "post_id", tags.Relation.Through.SourceKey, "源键应该是post_id")
		assert.Equal(t, "tag_id", tags.Relation.Through.TargetKey, "目标键应该是tag_id")

		// 验证中间表字段
		assert.NotNil(t, tags.Relation.Through.Fields, "应该有through.fields")
		createdAt := tags.Relation.Through.Fields["createdAt"]
		assert.NotNil(t, createdAt, "应该存在createdAt字段")
		assert.Equal(t, "created_at", createdAt.Column, "列名应该是created_at")
		assert.Equal(t, "标签添加时间", createdAt.Description, "描述应该正确")

		// 验证反向关系
		tag, exists := meta.Nodes["Tag"]
		assert.True(t, exists, "应该存在Tag类")

		posts := tag.Fields["posts"]
		assert.NotNil(t, posts, "应该存在posts字段")
		assert.NotNil(t, posts.Relation, "posts应该有关系定义")
		assert.Equal(t, internal.MANY_TO_MANY, posts.Relation.Type, "应该是多对多关系")
		assert.Equal(t, "Post", posts.Relation.TargetClass, "关系目标类应该是Post")
		assert.Equal(t, "tags", posts.Relation.TargetField, "关系目标字段应该是tags")

		// 验证双向关系
		assert.Same(t, tags.Relation.Reverse, posts.Relation, "Post.tags的反向关系应该是Tag.posts")
		assert.Same(t, posts.Relation.Reverse, tags.Relation, "Tag.posts的反向关系应该是Post.tags")
	})
}
