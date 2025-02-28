package metadata

import (
	"context"
	"fmt"
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// 测试 PostgreSQL 数据库
func TestPostgreSQL(t *testing.T) {
	versions := []string{"16", "15", "14", "13"}
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
				WaitingFor: wait.ForLog("database system is ready to accept connections"),
			}

			container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
				ContainerRequest: req,
				Started:          true,
			})
			require.NoError(t, err)
			defer container.Terminate(ctx)

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
	versions := []string{"8.0", "5.7"}

	for _, version := range versions {
		t.Run(fmt.Sprintf("Version %s", version), func(t *testing.T) {
			ctx := context.Background()
			req := testcontainers.ContainerRequest{
				Image:        fmt.Sprintf("docker.io/library/mysql:%s", version),
				ExposedPorts: []string{"3306/tcp"},
				Env: map[string]string{
					"MYSQL_ROOT_PASSWORD": "test",
					"MYSQL_DATABASE":      "test",
					"MYSQL_USER":          "test",
					"MYSQL_PASSWORD":      "test",
				},
				WaitingFor: wait.ForLog("port: 3306  MySQL Community Server"),
			}

			container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
				ContainerRequest: req,
				Started:          true,
			})
			require.NoError(t, err)
			defer container.Terminate(ctx)

			port, err := container.MappedPort(ctx, "3306")
			require.NoError(t, err)

			dsn := fmt.Sprintf("test:test@tcp(localhost:%d)/test?charset=utf8mb4&parseTime=True&loc=Local", port.Int())
			db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
			require.NoError(t, err)

			runDatabaseTests(t, db)
		})
	}
}

// 通用测试函数
func runDatabaseTests(t *testing.T, db *gorm.DB) {
	// 创建测试表
	var createTableSQL string
	if db.Dialector.Name() == "mysql" {
		createTableSQL = `
			CREATE TABLE users (
				id BIGINT AUTO_INCREMENT PRIMARY KEY,
				name VARCHAR(255) NOT NULL,
				email VARCHAR(255) UNIQUE NOT NULL,
				created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
			) COMMENT='用户表';

			CREATE TABLE posts (
				id BIGINT AUTO_INCREMENT PRIMARY KEY,
				title VARCHAR(255) NOT NULL COMMENT '标题',
				content TEXT COMMENT '内容',
				user_id BIGINT,
				created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (user_id) REFERENCES users(id)
			) COMMENT='文章表';

			ALTER TABLE users MODIFY COLUMN name VARCHAR(255) NOT NULL COMMENT '用户名';
			ALTER TABLE users MODIFY COLUMN email VARCHAR(255) NOT NULL COMMENT '邮箱';
		`
	} else {
		createTableSQL = `
			CREATE TABLE users (
				id SERIAL PRIMARY KEY,
				name VARCHAR(255) NOT NULL,
				email VARCHAR(255) UNIQUE NOT NULL,
				created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
			);

			CREATE TABLE posts (
				id SERIAL PRIMARY KEY,
				title VARCHAR(255) NOT NULL,
				content TEXT,
				user_id INTEGER REFERENCES users(id),
				created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
			);

			COMMENT ON TABLE users IS '用户表';
			COMMENT ON COLUMN users.name IS '用户名';
			COMMENT ON COLUMN users.email IS '邮箱';

			COMMENT ON TABLE posts IS '文章表';
			COMMENT ON COLUMN posts.title IS '标题';
			COMMENT ON COLUMN posts.content IS '内容';
			COMMENT ON COLUMN posts.user_id IS '作者ID';
		`
	}

	err := db.Exec(createTableSQL).Error
	require.NoError(t, err)

	// 创建 DatabaseLoader
	var schema string
	if db.Dialector.Name() == "mysql" {
		schema = "test"
	} else {
		schema = "public"
	}

	loader, err := NewDatabaseLoader(db, schema)
	require.NoError(t, err)

	// 加载元数据
	classes, relationships, err := loader.LoadMetadata()
	require.NoError(t, err)

	// 验证类信息
	t.Run("验证类信息", func(t *testing.T) {
		require.Len(t, classes, 2)

		users, ok := classes["users"]
		require.True(t, ok)
		require.Equal(t, "users", users.Table)
		require.Equal(t, "用户表", users.Description)

		require.Len(t, users.Fields, 4)
		require.Contains(t, users.Fields, "id")
		require.Contains(t, users.Fields, "name")
		require.Contains(t, users.Fields, "email")
		require.Contains(t, users.Fields, "created_at")

		nameField := users.Fields["name"]
		require.Equal(t, "name", nameField.Column)
		require.Equal(t, "用户名", nameField.Description)
		require.False(t, nameField.Nullable)

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
	})

	// 验证关系信息
	t.Run("验证关系信息", func(t *testing.T) {
		postsRelations, ok := relationships["posts"]
		require.True(t, ok)
		require.Len(t, postsRelations, 1)

		userIdFK, ok := postsRelations["user_id"]
		require.True(t, ok)
		require.Equal(t, "users", userIdFK.TableName)
		require.Equal(t, "id", userIdFK.ColumnName)
		require.Equal(t, internal.MANY_TO_ONE, userIdFK.Kind)
	})
}
