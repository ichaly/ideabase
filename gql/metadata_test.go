package gql

import (
	"context"
	"fmt"
	"os"
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
	"gorm.io/driver/mysql"
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
	err = db.Exec(`
		-- 创建注释表
		CREATE TABLE table_comments (
			table_name TEXT PRIMARY KEY,
			comment TEXT NOT NULL
		);

		CREATE TABLE column_comments (
			table_name TEXT NOT NULL,
			column_name TEXT NOT NULL,
			comment TEXT NOT NULL,
			PRIMARY KEY (table_name, column_name)
		);

		-- 创建业务表
		CREATE TABLE users (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT NOT NULL UNIQUE,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP
		);

		CREATE TABLE posts (
			id SERIAL PRIMARY KEY,
			title TEXT NOT NULL,
			content TEXT,
			user_id INTEGER NOT NULL REFERENCES users(id),
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE tags (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE post_tags (
			post_id INTEGER NOT NULL REFERENCES posts(id),
			tag_id INTEGER NOT NULL REFERENCES tags(id),
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (post_id, tag_id)
		);

		-- 插入表注释
		INSERT INTO table_comments (table_name, comment) VALUES
		('users', '用户表'),
		('posts', '文章表'),
		('tags', '标签表'),
		('post_tags', '文章标签关联表');

		-- 插入字段注释
		INSERT INTO column_comments (table_name, column_name, comment) VALUES
		('users', 'email', '邮箱');

		-- 设置表注释
		COMMENT ON TABLE users IS '用户表';
		COMMENT ON TABLE posts IS '文章表';
		COMMENT ON TABLE tags IS '标签表';
		COMMENT ON TABLE post_tags IS '文章标签关联表';

		-- 设置字段注释
		COMMENT ON COLUMN users.email IS '邮箱';
	`).Error
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
				assert.Equal(t, internal.MANY_TO_ONE, userId.Relation.Kind, "应该是many-to-one关系")
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
				assert.Equal(t, internal.MANY_TO_ONE, postId.Relation.Kind, "应该是many-to-one关系")
				assert.Equal(t, "posts", postId.Relation.TargetClass, "关系目标类应该是posts")
			}

			// 验证与Tags的关系
			tagId := postTags.GetField("tagId")
			assert.NotNil(t, tagId, "应该有tagId字段")
			if tagId != nil {
				assert.NotNil(t, tagId.Relation, "tagId应该有关系定义")
				assert.Equal(t, internal.MANY_TO_ONE, tagId.Relation.Kind, "应该是many-to-one关系")
				assert.Equal(t, "tags", tagId.Relation.TargetClass, "关系目标类应该是tags")
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
								assert.Equal(t, field1.Relation.Kind, field2.Relation.Kind, "关系类型应该相同")
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
	dbType := os.Getenv("TEST_DB_TYPE")
	if dbType == "" {
		t.Skip("跳过测试：未设置TEST_DB_TYPE环境变量")
	}

	schema := os.Getenv("TEST_DB_SCHEMA")
	if schema == "" {
		t.Skip("跳过测试：未设置TEST_DB_SCHEMA环境变量")
	}

	var dialector gorm.Dialector
	switch dbType {
	case "mysql":
		dsn := os.Getenv("TEST_MYSQL_DSN")
		if dsn == "" {
			t.Skip("跳过测试：未设置TEST_MYSQL_DSN环境变量")
		}

		dialector = mysql.Open(dsn)
	case "postgres":
		dsn := os.Getenv("TEST_PGSQL_DSN")
		if dsn == "" {
			t.Skip("跳过测试：未设置TEST_PGSQL_DSN环境变量")
		}

		dialector = postgres.Open(dsn)
	default:
		t.Fatalf("不支持的数据库类型: %s", dbType)
	}
	db, err := gorm.Open(dialector, &gorm.Config{})
	require.NoError(t, err, "连接数据库失败")

	// 创建配置
	v := viper.New()
	v.Set("mode", "dev")
	v.Set("app.root", utl.Root())
	v.Set("schema.schema", schema)
	v.Set("schema.enable-camel-case", true)

	// 创建元数据加载器
	meta, err := NewMetadata(v, db)
	require.NoError(t, err, "创建元数据加载器失败")

	// 验证元数据已加载
	assert.NotEmpty(t, meta.Nodes, "应该有加载的类")
}

// 测试配置加载
func TestLoadMetadataFromConfig(t *testing.T) {
	// 创建配置
	v := viper.New()
	v.Set("mode", "dev")
	v.Set("app.root", utl.Root())
	v.Set("schema.enable-camel-case", true)

	// 设置测试元数据配置
	v.Set("metadata.tables", []map[string]interface{}{
		{
			"name":         "users",
			"display_name": "User",
			"description":  "用户表",
			"primary_keys": []string{"id"},
			"columns": []map[string]interface{}{
				{
					"name":         "id",
					"display_name": "id",
					"type":         "integer",
					"is_primary":   true,
				},
				{
					"name":         "username",
					"display_name": "name",
					"type":         "character varying",
					"description":  "用户名",
				},
			},
		},
	})

	// 创建元数据加载器
	meta, err := NewMetadata(v, nil)
	require.NoError(t, err, "创建元数据加载器失败")

	// 验证元数据已加载
	assert.Len(t, meta.Nodes, 2, "应该有2个Node索引")

	// 通过类名查找
	userNode, ok := meta.Nodes["User"]
	assert.True(t, ok, "应该能通过类名找到Node")
	assert.Equal(t, "User", userNode.Name, "类名应该正确")
	assert.Equal(t, "users", userNode.Table, "表名应该正确")
	assert.Len(t, userNode.Fields, 2, "应该有2个字段")

	// 通过表名查找
	tableNode, ok := meta.Nodes["users"]
	assert.True(t, ok, "应该能通过表名找到Node")
	assert.Same(t, userNode, tableNode, "通过类名和表名找到的应该是同一个Node")
}

// 测试名称转换
func TestNameConversion(t *testing.T) {
	// 创建配置
	v := viper.New()
	v.Set("mode", "dev")
	v.Set("app.root", utl.Root())
	v.Set("schema.enable-camel-case", true)
	v.Set("schema.table-prefix", []string{"tbl_"})

	// 设置测试元数据配置
	v.Set("metadata.tables", []map[string]interface{}{
		{
			"name": "tbl_user_profiles",
			"columns": []map[string]interface{}{
				{
					"name": "user_id",
					"type": "integer",
				},
				{
					"name": "first_name",
					"type": "character varying",
				},
			},
		},
	})

	// 创建元数据加载器
	meta, err := NewMetadata(v, nil)
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
	// 创建配置
	v := viper.New()
	v.Set("mode", "dev")
	v.Set("app.root", utl.Root())
	v.Set("schema.include-tables", []string{"users"})
	v.Set("schema.exclude-fields", []string{"password"})

	// 设置测试元数据配置
	v.Set("metadata.tables", []map[string]interface{}{
		{
			"name": "users",
			"columns": []map[string]interface{}{
				{
					"name": "id",
					"type": "integer",
				},
				{
					"name": "name",
					"type": "character varying",
				},
				{
					"name": "password",
					"type": "character varying",
				},
			},
		},
		{
			"name": "posts",
			"columns": []map[string]interface{}{
				{
					"name": "id",
					"type": "integer",
				},
			},
		},
	})

	// 创建元数据加载器
	meta, err := NewMetadata(v, nil)
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
	// 创建配置
	v := viper.New()
	v.Set("mode", "dev")
	v.Set("app.root", utl.Root())

	// 创建元数据加载器
	meta, err := NewMetadata(v, nil)
	require.NoError(t, err, "创建元数据加载器失败")

	// 验证基本信息
	assert.NotEmpty(t, meta.Version, "版本号不应为空")
	assert.Len(t, meta.Version, 14, "版本号应该是14位时间戳")
	assert.Len(t, meta.Nodes, 10, "应该有10个Node索引(5个类，每个类有类名和表名两个索引)")

	// 测试Users类
	users, ok := meta.Nodes["Users"]
	assert.True(t, ok, "应该能找到Users类")
	assert.Equal(t, "Users", users.Name, "类名应该是Users")
	assert.Equal(t, "users", users.Table, "表名应该是users")
	assert.Equal(t, "用户表", users.Description, "描述应该正确")
	assert.Equal(t, []string{"id"}, users.PrimaryKeys, "主键应该正确")

	// 测试Users的字段
	assert.Len(t, users.Fields, 5, "Users应该有5个字段索引(4个字段，其中createdAt字段有额外的列名索引)")

	// 测试email字段（通过字段名访问）
	email := users.GetField("email")
	assert.NotNil(t, email, "应该能通过字段名找到email字段")
	assert.Contains(t, []string{"character varying", "varchar"}, email.Type, "email字段类型应该正确")
	assert.Equal(t, "邮箱", email.Description, "email字段描述应该正确")
	assert.False(t, email.IsPrimary, "email不应该是主键")
	assert.False(t, email.Nullable, "email不应该可为空")

	// 测试createdAt字段的双重索引
	createdAt := users.GetField("createdAt")
	assert.NotNil(t, createdAt, "应该能通过字段名找到createdAt字段")
	createdAtByColumn := users.GetField("created_at")
	assert.NotNil(t, createdAtByColumn, "应该能通过列名找到created_at字段")
	assert.Same(t, createdAt, createdAtByColumn, "通过字段名和列名获取的应该是同一个字段")

	// 测试关系
	posts, ok := meta.Nodes["Posts"]
	assert.True(t, ok, "应该能找到Posts类")
	userId := posts.GetField("userId")
	assert.NotNil(t, userId, "应该能通过字段名找到userId字段")
	userIdByColumn := posts.GetField("user_id")
	assert.NotNil(t, userIdByColumn, "应该能通过列名找到user_id字段")
	assert.Same(t, userId, userIdByColumn, "通过字段名和列名获取的应该是同一个字段")
	assert.NotNil(t, userId.Relation, "userId应该有关系定义")
	assert.Equal(t, "many_to_one", string(userId.Relation.Kind), "应该是many_to_one关系")
	assert.Equal(t, "users", userId.Relation.TargetClass, "关系目标类应该是users")
	assert.Equal(t, "id", userId.Relation.TargetField, "关系目标字段应该是id")

	// 测试多对多关系
	postTags, ok := meta.Nodes["PostTags"]
	assert.True(t, ok, "应该能找到PostTags类")
	assert.Equal(t, []string{"postId", "tagId"}, postTags.PrimaryKeys, "PostTags应该有两个主键")

	// 测试PostTags的关系字段双重索引
	tagId := postTags.GetField("tagId")
	assert.NotNil(t, tagId, "应该能通过字段名找到tagId字段")
	tagIdByColumn := postTags.GetField("tag_id")
	assert.NotNil(t, tagIdByColumn, "应该能通过列名找到tag_id字段")
	assert.Same(t, tagId, tagIdByColumn, "通过字段名和列名获取的应该是同一个字段")
	assert.NotNil(t, tagId.Relation, "tagId应该有关系定义")
	assert.Equal(t, "many_to_one", string(tagId.Relation.Kind), "应该是many_to_one关系")
	assert.Equal(t, "tags", tagId.Relation.TargetClass, "关系目标类应该是tags")
}
