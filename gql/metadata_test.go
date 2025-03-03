package gql

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	// 跳过测试，如果数据库连接环境变量没设置
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		t.Skip("跳过测试：未设置TEST_DB_DSN环境变量")
	}

	// 连接数据库
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err, "连接数据库失败")

	// 创建配置
	v := viper.New()
	v.Set("schema.source", internal.SourceDatabase)
	v.Set("schema.schema", "public")
	v.Set("schema.enable-camel-case", true)
	v.Set("schema.enable-cache", true)

	// 创建元数据加载器
	meta, err := NewMetadata(v, db)
	require.NoError(t, err, "创建元数据加载器失败")

	// 验证元数据已加载
	assert.NotEmpty(t, meta.Nodes, "应该有加载的类")
}

// 测试文件加载
func TestLoadMetadataFromFile(t *testing.T) {
	// 创建临时文件
	tempDir := t.TempDir()
	cachePath := filepath.Join(tempDir, "metadata_cache.json")

	// 创建测试元数据
	userClass := &internal.Class{
		Name:    "User",
		Table:   "users",
		Virtual: false,
		Fields: map[string]*internal.Field{
			"id": {
				Name:      "id",
				Column:    "id",
				Type:      "integer",
				Virtual:   false,
				IsPrimary: true,
			},
			"name": {
				Name:    "name",
				Column:  "name",
				Type:    "character varying",
				Virtual: false,
			},
		},
		PrimaryKeys: []string{"id"},
		TableNames:  map[string]bool{"users": false},
	}

	testCache := MetadataCache{
		Nodes: map[string]*internal.Class{
			"User":  userClass,
			"users": userClass,
		},
	}

	// 写入临时文件
	data, err := json.MarshalIndent(testCache, "", "  ")
	require.NoError(t, err, "序列化测试缓存失败")
	err = os.WriteFile(cachePath, data, 0644)
	require.NoError(t, err, "写入临时文件失败")

	// 创建配置
	v := viper.New()
	v.Set("schema.source", internal.SourceFile)
	v.Set("schema.cache-path", cachePath)
	v.Set("schema.enable-camel-case", true)

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
	assert.True(t, origTableNode.TableNames["tbl_user_profiles"], "原始表名标记应该为true")
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
