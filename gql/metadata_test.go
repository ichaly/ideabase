package gql

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

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
	v.Set("schema.enable-cache", false)

	// 创建元数据加载器
	meta, err := NewMetadata(v, db)
	require.NoError(t, err, "创建元数据加载器失败")

	// 验证元数据已加载
	assert.NotEmpty(t, meta.Nodes, "应该有加载的类")
	assert.NotEmpty(t, meta.tableToClass, "应该有表名到类名的映射")
}

// 测试文件加载
func TestLoadMetadataFromFile(t *testing.T) {
	// 创建临时文件
	tempDir := t.TempDir()
	cachePath := filepath.Join(tempDir, "metadata_cache.json")

	// 创建测试元数据
	testCache := MetadataCache{
		Classes: []*internal.Class{
			{
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
			},
		},
		Relationships: map[string]map[string]*internal.ForeignKey{},
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
	assert.Len(t, meta.Nodes, 1, "应该有1个加载的类")
	assert.Contains(t, meta.Nodes, "User", "应该包含User类")
	assert.Len(t, meta.Nodes["User"].Fields, 2, "User类应该有2个字段")
	assert.Contains(t, meta.Nodes["User"].Fields, "id", "应该包含id字段")
	assert.Contains(t, meta.Nodes["User"].Fields, "name", "应该包含name字段")
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
	assert.Len(t, meta.Nodes, 1, "应该有1个加载的类")
	assert.Contains(t, meta.Nodes, "User", "应该包含User类")
	assert.Len(t, meta.Nodes["User"].Fields, 2, "User类应该有2个字段")
	assert.Contains(t, meta.Nodes["User"].Fields, "id", "应该包含id字段")
	assert.Contains(t, meta.Nodes["User"].Fields, "name", "应该包含name字段")
}

// 测试名称转换
func TestNameConversion(t *testing.T) {
	// 创建配置
	v := viper.New()
	v.Set("schema.source", internal.SourceConfig)
	v.Set("schema.enable-camel-case", true)
	v.Set("schema.table-prefix", "tbl_")

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
	assert.Contains(t, meta.Nodes, "UserProfiles", "应该去除前缀并转为驼峰命名")
	assert.Contains(t, meta.Nodes["UserProfiles"].Fields, "userId", "应该转换为驼峰命名")
	assert.Contains(t, meta.Nodes["UserProfiles"].Fields, "firstName", "应该转换为驼峰命名")
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
				{
					"name": "title",
					"type": "character varying",
				},
			},
		},
	})

	// 创建元数据加载器
	meta, err := NewMetadata(v, nil)
	require.NoError(t, err, "创建元数据加载器失败")

	// 验证表过滤
	assert.Contains(t, meta.Nodes, "users", "应该包含users表")
	assert.NotContains(t, meta.Nodes, "posts", "不应该包含posts表")

	// 验证字段过滤
	assert.Contains(t, meta.Nodes["users"].Fields, "id", "应该包含id字段")
	assert.Contains(t, meta.Nodes["users"].Fields, "name", "应该包含name字段")
	assert.NotContains(t, meta.Nodes["users"].Fields, "password", "不应该包含password字段")
}
