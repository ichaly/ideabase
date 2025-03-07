package gql

import (
	"context"
	"encoding/json"
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
			userId := posts.GetField("userId")
			assert.NotNil(t, userId, "应该有userId字段")
			if userId != nil {
				assert.NotNil(t, userId.Relation, "userId应该有关系定义")
				assert.Equal(t, internal.MANY_TO_ONE, userId.Relation.Type, "应该是many-to-one关系")
				assert.Equal(t, "users", userId.Relation.TargetClass, "关系目标类应该是users")
			}
		}

		// 验证many-to-many关系
		postTags, exists := meta.Nodes["PostTags"]
		assert.True(t, exists, "应该存在PostTags表")
		if exists {
			// 验证与Posts的关系
			postId := postTags.GetField("postId")
			assert.NotNil(t, postId, "应该有postId字段")
			if postId != nil {
				assert.NotNil(t, postId.Relation, "postId应该有关系定义")
				assert.Equal(t, internal.MANY_TO_ONE, postId.Relation.Type, "应该是many-to-one关系")
				assert.Equal(t, "posts", postId.Relation.TargetClass, "关系目标类应该是posts")
			}

			// 验证与Tags的关系
			tagId := postTags.GetField("tagId")
			assert.NotNil(t, tagId, "应该有tagId字段")
			if tagId != nil {
				assert.NotNil(t, tagId.Relation, "tagId应该有关系定义")
				assert.Equal(t, internal.MANY_TO_ONE, tagId.Relation.Type, "应该是many-to-one关系")
				assert.Equal(t, "tags", tagId.Relation.TargetClass, "关系目标类应该是tags")
			}
		}

		// 验证自关联关系
		organizations, exists := meta.Nodes["Organizations"]
		assert.True(t, exists, "应该存在Organizations表")
		if exists {
			// 验证parentId字段
			parentId := organizations.GetField("parentId")
			assert.NotNil(t, parentId, "应该有parentId字段")
			if parentId != nil {
				assert.NotNil(t, parentId.Relation, "parentId应该有关系定义")
				assert.Equal(t, internal.RECURSIVE, parentId.Relation.Type, "应该是recursive关系")
				assert.Equal(t, "organizations", parentId.Relation.TargetClass, "关系目标类应该是organizations")
				assert.Equal(t, "id", parentId.Relation.TargetField, "关系目标字段应该是id")

				// 验证反向关系
				assert.NotNil(t, parentId.Relation.Reverse, "应该有反向关系")
				if parentId.Relation.Reverse != nil {
					assert.Equal(t, internal.RECURSIVE, parentId.Relation.Reverse.Type, "反向关系也应该是recursive")
					assert.Equal(t, "organizations", parentId.Relation.Reverse.TargetClass, "反向关系目标类应该是organizations")
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
		v.Set("metadata.tables", map[string]*internal.TableConfig{
			"users": {
				Description: "用户表",
				Columns: map[string]*internal.ColumnConfig{
					"id": {
						Name:        "id",
						Type:        "int",
						Description: "用户ID",
						IsPrimary:   true,
					},
					"name": {
						Name:        "name",
						Type:        "string",
						Description: "用户名",
					},
				},
			},
			"virtual_table": {
				Description: "虚拟表",
				Virtual:     true,
				Columns: map[string]*internal.ColumnConfig{
					"id": {
						Name:      "id",
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
		users, exists := loader.Nodes["users"]
		require.True(t, exists, "应该存在users表")
		require.Equal(t, "用户表", users.Description, "表描述应该正确")

		id, exists := users.Fields["id"]
		require.True(t, exists, "应该存在id字段")
		require.Equal(t, "用户ID", id.Description, "字段描述应该正确")
		require.True(t, id.IsPrimary, "id应该是主键")

		name, exists := users.Fields["name"]
		require.True(t, exists, "应该存在name字段")
		require.Equal(t, "用户名", name.Description, "字段描述应该正确")

		vt, exists := loader.Nodes["virtual_table"]
		require.True(t, exists, "应该存在virtual_table表")
		require.Equal(t, "虚拟表", vt.Description, "虚拟表描述应该正确")
		require.True(t, vt.Virtual, "virtual_table应该是虚拟表")
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
	v.Set("database.driver", "postgres") // 设置数据库驱动类型

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
						"type":        "character varying",
						"description": "用户名",
					},
					"password": map[string]interface{}{
						"name":        "password",
						"type":        "character varying",
						"description": "密码",
					},
				},
			},
		},
	}
	configBytes, err := json.MarshalIndent(configData, "", "  ")
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
	v.Set("database.driver", "postgres")           // 设置数据库驱动类型
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
	assert.Len(t, userNode.Fields, 2, "应该有2个字段")

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
	v.Set("database.driver", "postgres") // 设置数据库驱动类型

	// 设置测试元数据配置
	v.Set("metadata.tables", map[string]interface{}{
		"tbl_user_profiles": map[string]interface{}{
			"columns": map[string]interface{}{
				"user_id": map[string]interface{}{
					"type": "integer",
				},
				"first_name": map[string]interface{}{
					"type": "character varying",
				},
			},
		},
	})

	// 创建元数据加载器
	meta, err := NewMetadata(v, db)
	require.NoError(t, err, "创建元数据加载器失败")

	// 验证名称转换
	userProfilesNode, ok := meta.Nodes["UserProfiles"]
	assert.True(t, ok, "应该能通过转换后的类名找到Node")
	assert.Contains(t, userProfilesNode.Fields, "userId", "应该转换为驼峰命名")
	assert.Contains(t, userProfilesNode.Fields, "firstName", "应该转换为驼峰命名")

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
	v.Set("database.driver", "postgres") // 设置数据库驱动类型

	// 设置测试元数据配置
	v.Set("metadata.tables", map[string]interface{}{
		"users": map[string]interface{}{
			"columns": map[string]interface{}{
				"id": map[string]interface{}{
					"type": "integer",
				},
				"name": map[string]interface{}{
					"type": "character varying",
				},
				"password": map[string]interface{}{
					"type": "character varying",
				},
			},
		},
		"posts": map[string]interface{}{
			"columns": map[string]interface{}{
				"id": map[string]interface{}{
					"type": "integer",
				},
			},
		},
	})

	// 创建元数据加载器
	meta, err := NewMetadata(v, db)
	require.NoError(t, err, "创建元数据加载器失败")

	// 验证表过滤
	assert.Len(t, meta.Nodes, 2, "应该只有users表的两个索引")
	_, ok := meta.Nodes["posts"]
	assert.False(t, ok, "posts表应该被过滤掉")

	// 验证字段过滤
	usersNode, ok := meta.Nodes["users"]
	assert.True(t, ok, "应该能找到users表")
	assert.NotContains(t, usersNode.Fields, "password", "password字段应该被过滤掉")
	assert.Contains(t, usersNode.Fields, "name", "name字段应该保留")
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
	v.Set("database.driver", "postgres") // 设置数据库驱动类型
	v.Set("metadata.mode", "file")       // 设置从文件加载元数据

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
