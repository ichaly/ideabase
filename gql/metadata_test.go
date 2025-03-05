package gql

import (
	"os"
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	v.Set("schema.source", internal.SourceDatabase)
	v.Set("schema.schema", schema)
	v.Set("schema.enable-camel-case", true)
	v.Set("schema.enable-cache", true)

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
	v.Set("schema.source", internal.SourceConfig)
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
	v.Set("schema.source", internal.SourceConfig)
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
	v.Set("schema.source", internal.SourceConfig)
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
	v.Set("schema.source", internal.SourceFile)
	v.Set("schema.cache-path", "../cfg/metadata.json")

	// 创建元数据加载器
	meta, err := NewMetadata(v, nil)
	require.NoError(t, err, "创建元数据加载器失败")

	// 验证基本信息
	assert.Equal(t, "20250305170531", meta.Version, "版本号应该匹配")
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
	assert.Equal(t, "character varying", email.Type, "email字段类型应该正确")
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
