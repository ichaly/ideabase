package metadata

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/utl"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// 测试 PostgreSQL 数据库
func TestPostgreSQL(t *testing.T) {
	versions := []string{"16"}

	for _, version := range versions {
		t.Run(fmt.Sprintf("Version %s", version), func(t *testing.T) {
			ctx := context.Background()
			req := testcontainers.ContainerRequest{
				Image:        fmt.Sprintf("docker.io/library/postgres:%s", version),
				ExposedPorts: []string{"5432/tcp"},
				Env: map[string]string{
					"POSTGRES_USER":     "test",
					"POSTGRES_PASSWORD": "test",
					"POSTGRES_DB":       "test",
				},
				WaitingFor: wait.ForAll(
					wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
					wait.ForListeningPort("5432/tcp"),
				),
			}

			container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
				ContainerRequest: req,
				Started:          true,
			})
			require.NoError(t, err)
			defer container.Terminate(ctx)

			// 等待一小段时间确保数据库完全就绪
			time.Sleep(2 * time.Second)

			port, err := container.MappedPort(ctx, "5432")
			require.NoError(t, err)

			dsn := fmt.Sprintf("host=localhost port=%d user=test password=test dbname=test sslmode=disable", port.Int())
			db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
			require.NoError(t, err)

			runDatabaseTests(t, db)
		})
	}
}

// 测试 MySQL 数据库
func TestMySQL(t *testing.T) {
	versions := []string{"8.0"}

	for _, version := range versions {
		t.Run(fmt.Sprintf("Version %s", version), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			req := testcontainers.ContainerRequest{
				Image:        fmt.Sprintf("docker.io/library/mysql:%s", version),
				ExposedPorts: []string{"3306/tcp"},
				Env: map[string]string{
					"MYSQL_ROOT_PASSWORD": "test",
					"MYSQL_DATABASE":      "test",
					"MYSQL_USER":          "test",
					"MYSQL_PASSWORD":      "test",
				},
				WaitingFor: wait.ForAll(
					wait.ForLog("MySQL Community Server - GPL"),
					wait.ForListeningPort("3306/tcp"),
				),
			}

			container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
				ContainerRequest: req,
				Started:          true,
			})
			require.NoError(t, err)
			defer container.Terminate(ctx)

			port, err := container.MappedPort(ctx, "3306")
			require.NoError(t, err)

			// 等待额外的时间以确保数据库完全就绪
			time.Sleep(10 * time.Second)

			host, err := container.Host(ctx)
			require.NoError(t, err)
			dsn := fmt.Sprintf("test:test@tcp(%s:%d)/test?charset=utf8mb4&parseTime=True&loc=Local", host, port.Int())
			db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
			require.NoError(t, err)

			runDatabaseTests(t, db)
		})
	}
}

// 通用测试函数
func runDatabaseTests(t *testing.T, db *gorm.DB) {
	// 创建测试表
	var sqlFile string
	if db.Dialector.Name() == "mysql" {
		sqlFile = "mysql.sql"
	} else {
		sqlFile = "pgsql.sql"
	}

	// 读取SQL文件
	sqlBytes, err := os.ReadFile(filepath.Join(utl.Root(), "gql/assets/sql/", sqlFile))
	require.NoError(t, err, "读取SQL文件失败")

	// 执行SQL语句
	if db.Dialector.Name() == "mysql" {
		// MySQL需要按照依赖顺序执行语句
		sqlStatements := strings.Split(string(sqlBytes), ";")
		// 查找并执行创建表语句
		for _, stmt := range sqlStatements {
			if strings.TrimSpace(stmt) == "" {
				continue
			}
			// 执行创建表语句
			if err := db.Exec(stmt).Error; err != nil {
				t.Logf("执行SQL失败: %v\nSQL: %s", err, stmt)
				require.NoError(t, err)
			}
		}
	} else {
		// PostgreSQL可以一次执行多个语句
		err = db.Exec(string(sqlBytes)).Error
		require.NoError(t, err)
	}

	// 创建 DatabaseLoader
	var schema string
	if db.Dialector.Name() == "mysql" {
		schema = "test"
	} else {
		schema = "public"
	}

	loader, err := NewSchemaInspector(db, schema)
	require.NoError(t, err)

	// 加载元数据
	classes, err := loader.LoadMetadata()
	require.NoError(t, err)

	// 验证类信息
	t.Run("验证类信息", func(t *testing.T) {
		require.Len(t, classes, 5) // 现在应该有5个类：users, posts, comments, tags, post_tags

		// 验证users表
		users, ok := classes["users"]
		require.True(t, ok)
		require.Equal(t, "users", users.Table)
		require.Equal(t, "用户表", users.Description)
		require.Len(t, users.Fields, 5)
		require.Contains(t, users.Fields, "id")
		require.Contains(t, users.Fields, "name")
		require.Contains(t, users.Fields, "email")
		require.Contains(t, users.Fields, "created_at")

		nameField := users.Fields["name"]
		require.Equal(t, "name", nameField.Column)
		require.Equal(t, "用户名", nameField.Description)
		require.False(t, nameField.Nullable)

		// 验证posts表
		posts, ok := classes["posts"]
		require.True(t, ok)
		require.Equal(t, "posts", posts.Table)
		require.Equal(t, "文章表", posts.Description)
		require.Len(t, posts.Fields, 5)
		require.Contains(t, posts.Fields, "id")
		require.Contains(t, posts.Fields, "title")
		require.Contains(t, posts.Fields, "content")
		require.Contains(t, posts.Fields, "user_id")
		require.Contains(t, posts.Fields, "created_at")

		// 验证comments表
		comments, ok := classes["comments"]
		require.True(t, ok)
		require.Equal(t, "comments", comments.Table)
		require.Equal(t, "评论表", comments.Description)
		require.Len(t, comments.Fields, 6)
		require.Contains(t, comments.Fields, "id")
		require.Contains(t, comments.Fields, "content")
		require.Contains(t, comments.Fields, "user_id")
		require.Contains(t, comments.Fields, "post_id")
		require.Contains(t, comments.Fields, "parent_id")
		require.Contains(t, comments.Fields, "created_at")

		// 验证关系
		userIdField := comments.Fields["user_id"]
		require.NotNil(t, userIdField.Relation)
		require.Equal(t, "users", userIdField.Relation.TargetClass)
		require.Equal(t, "id", userIdField.Relation.TargetField)
		require.Equal(t, internal.MANY_TO_ONE, userIdField.Relation.Type)

		postIdField := comments.Fields["post_id"]
		require.NotNil(t, postIdField.Relation)
		require.Equal(t, "posts", postIdField.Relation.TargetClass)
		require.Equal(t, "id", postIdField.Relation.TargetField)
		require.Equal(t, internal.MANY_TO_ONE, postIdField.Relation.Type)

		// 验证tags表
		tags, ok := classes["tags"]
		require.True(t, ok)
		require.Equal(t, "tags", tags.Table)
		require.Equal(t, "标签表", tags.Description)
		require.Len(t, tags.Fields, 3)
		require.Contains(t, tags.Fields, "id")
		require.Contains(t, tags.Fields, "name")
		require.Contains(t, tags.Fields, "created_at")

		// 验证post_tags表
		postTags, ok := classes["post_tags"]
		require.True(t, ok)
		require.Equal(t, "post_tags", postTags.Table)
		require.Equal(t, "文章标签关联表", postTags.Description)
		require.Len(t, postTags.Fields, 3)
		require.Contains(t, postTags.Fields, "post_id")
		require.Contains(t, postTags.Fields, "tag_id")
		require.Contains(t, postTags.Fields, "created_at")
	})
}

// 测试多对多关系检测
func TestDetectManyToManyRelations(t *testing.T) {
	// 创建测试数据
	classes := make(map[string]*internal.Class)

	// 创建 users 类
	users := &internal.Class{
		Name:        "users",
		Table:       "users",
		PrimaryKeys: []string{"id"},
		Fields: map[string]*internal.Field{
			"id": {
				Name:      "id",
				Column:    "id",
				Type:      "integer",
				IsPrimary: true,
			},
		},
	}
	classes["users"] = users

	// 创建 roles 类
	roles := &internal.Class{
		Name:        "roles",
		Table:       "roles",
		PrimaryKeys: []string{"id"},
		Fields: map[string]*internal.Field{
			"id": {
				Name:      "id",
				Column:    "id",
				Type:      "integer",
				IsPrimary: true,
			},
		},
	}
	classes["roles"] = roles

	// 创建 user_roles 类
	userRoles := &internal.Class{
		Name:        "user_roles",
		Table:       "user_roles",
		PrimaryKeys: []string{"user_id", "role_id"},
		Fields: map[string]*internal.Field{
			"user_id": {
				Name:   "user_id",
				Column: "user_id",
				Type:   "integer",
			},
			"role_id": {
				Name:   "role_id",
				Column: "role_id",
				Type:   "integer",
			},
		},
	}
	classes["user_roles"] = userRoles

	// 创建外键信息
	foreignKeys := []foreignKeyInfo{
		{
			SourceTable:  "user_roles",
			SourceColumn: "user_id",
			TargetTable:  "users",
			TargetColumn: "id",
		},
		{
			SourceTable:  "user_roles",
			SourceColumn: "role_id",
			TargetTable:  "roles",
			TargetColumn: "id",
		},
	}

	// 创建主键信息
	primaryKeys := []primaryKeyInfo{
		{
			TableName:  "users",
			ColumnName: "id",
		},
		{
			TableName:  "roles",
			ColumnName: "id",
		},
		{
			TableName:  "user_roles",
			ColumnName: "user_id",
		},
		{
			TableName:  "user_roles",
			ColumnName: "role_id",
		},
	}

	// 创建加载器并检测多对多关系
	loader := &SchemaInspector{}
	loader.detectManyToManyRelations(classes, foreignKeys, primaryKeys)

	// 在新的实现中，我们不再创建虚拟字段，而是将关系信息添加到现有字段中
	// 验证 users 类的 id 字段是否有关系信息
	idField := classes["users"].Fields["id"]
	assert.NotNil(t, idField.Relation, "users.id 应该有关系定义")
	if idField.Relation != nil {
		assert.Equal(t, internal.MANY_TO_MANY, idField.Relation.Type, "关系类型应该是多对多")
		assert.Equal(t, "users", idField.Relation.SourceClass, "源类应该是 users")
		assert.Equal(t, "id", idField.Relation.SourceField, "源字段应该是 id")
		assert.Equal(t, "roles", idField.Relation.TargetClass, "目标类应该是 roles")
		assert.Equal(t, "id", idField.Relation.TargetField, "目标字段应该是 id")
		assert.NotNil(t, idField.Relation.Through, "关系应该有Through配置")
		if idField.Relation.Through != nil {
			assert.Equal(t, "user_roles", idField.Relation.Through.Table, "中间表应该是 user_roles")
			assert.Equal(t, "user_id", idField.Relation.Through.SourceKey, "源键应该是 user_id")
			assert.Equal(t, "role_id", idField.Relation.Through.TargetKey, "目标键应该是 role_id")
		}
	}

	// 验证 roles 类的 id 字段是否有关系信息
	idField = classes["roles"].Fields["id"]
	assert.NotNil(t, idField.Relation, "roles.id 应该有关系定义")
	if idField.Relation != nil {
		assert.Equal(t, internal.MANY_TO_MANY, idField.Relation.Type, "关系类型应该是多对多")
		assert.Equal(t, "roles", idField.Relation.SourceClass, "源类应该是 roles")
		assert.Equal(t, "id", idField.Relation.SourceField, "源字段应该是 id")
		assert.Equal(t, "users", idField.Relation.TargetClass, "目标类应该是 users")
		assert.Equal(t, "id", idField.Relation.TargetField, "目标字段应该是 id")
		assert.NotNil(t, idField.Relation.Through, "关系应该有Through配置")
		if idField.Relation.Through != nil {
			assert.Equal(t, "user_roles", idField.Relation.Through.Table, "中间表应该是 user_roles")
			assert.Equal(t, "role_id", idField.Relation.Through.SourceKey, "源键应该是 role_id")
			assert.Equal(t, "user_id", idField.Relation.Through.TargetKey, "目标键应该是 user_id")
		}
	}
}

// 测试表名匹配函数
func TestIsThroughTableByName(t *testing.T) {
	tests := []struct {
		tableName string
		table1    string
		table2    string
		expected  bool
	}{
		{"users_roles", "users", "roles", true},
		{"roles_users", "users", "roles", true},
		{"users_roles", "roles", "users", true},
		{"roles_users", "roles", "users", true},
		{"users_permissions", "users", "roles", false},
		{"user_role", "users", "roles", false},
		{"users", "users", "roles", false},
	}

	for _, tt := range tests {
		t.Run(tt.tableName, func(t *testing.T) {
			result := isThroughTableByName(tt.tableName, tt.table1, tt.table2)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// 测试元素比较函数
func TestContainsSameElements(t *testing.T) {
	tests := []struct {
		a        []string
		b        []string
		expected bool
	}{
		{[]string{"a", "b"}, []string{"a", "b"}, true},
		{[]string{"a", "b"}, []string{"b", "a"}, true},
		{[]string{"a", "b", "c"}, []string{"a", "b"}, false},
		{[]string{"a", "b"}, []string{"a", "c"}, false},
		{[]string{}, []string{}, true},
		{nil, nil, true},
		{[]string{"a", "a"}, []string{"a", "a"}, true},
		{[]string{"a", "a"}, []string{"a", "b"}, false},
	}

	for i, tt := range tests {
		t.Run(fmt.Sprintf("test-%d", i), func(t *testing.T) {
			result := containsSameElements(tt.a, tt.b)
			assert.Equal(t, tt.expected, result)
		})
	}
}
