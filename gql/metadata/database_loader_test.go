package metadata

import (
	"context"
	"fmt"
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestDatabaseLoader(t *testing.T) {
	// 启动 PostgreSQL 容器
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        "postgres:latest",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "test",
			"POSTGRES_PASSWORD": "test",
			"POSTGRES_DB":       "test",
		},
		WaitingFor: wait.ForLog("database system is ready to accept connections"),
	}

	postgresC, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)
	defer func() {
		if err := postgresC.Terminate(ctx); err != nil {
			t.Fatalf("failed to terminate container: %s", err)
		}
	}()

	// 获取容器端口
	port, err := postgresC.MappedPort(ctx, "5432")
	require.NoError(t, err)

	// 构建 DSN
	dsn := fmt.Sprintf("host=localhost port=%d user=test password=test dbname=test sslmode=disable", port.Int())

	// 连接数据库
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)

	// 创建测试表
	err = db.Exec(`
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
	`).Error
	require.NoError(t, err)

	// 创建 DatabaseLoader
	loader, err := NewDatabaseLoader(db, "public")
	require.NoError(t, err)

	// 加载元数据
	classes, relationships, err := loader.LoadMetadata()
	require.NoError(t, err)

	// 验证类信息
	t.Run("验证类信息", func(t *testing.T) {
		// 检查是否有两个类
		require.Len(t, classes, 2)

		// 验证 users 表
		users, ok := classes["users"]
		require.True(t, ok)
		require.Equal(t, "users", users.Table)
		require.Equal(t, "用户表", users.Description)

		// 验证 users 表的字段
		require.Len(t, users.Fields, 4)
		require.Contains(t, users.Fields, "id")
		require.Contains(t, users.Fields, "name")
		require.Contains(t, users.Fields, "email")
		require.Contains(t, users.Fields, "created_at")

		// 验证字段属性
		nameField := users.Fields["name"]
		require.Equal(t, "name", nameField.Column)
		require.Equal(t, "用户名", nameField.Description)
		require.False(t, nameField.Nullable)

		// 验证 posts 表
		posts, ok := classes["posts"]
		require.True(t, ok)
		require.Equal(t, "posts", posts.Table)
		require.Equal(t, "文章表", posts.Description)

		// 验证 posts 表的字段
		require.Len(t, posts.Fields, 5)
		require.Contains(t, posts.Fields, "id")
		require.Contains(t, posts.Fields, "title")
		require.Contains(t, posts.Fields, "content")
		require.Contains(t, posts.Fields, "user_id")
		require.Contains(t, posts.Fields, "created_at")
	})

	// 验证关系信息
	t.Run("验证关系信息", func(t *testing.T) {
		// 检查 posts 表的外键关系
		postsRelations, ok := relationships["posts"]
		require.True(t, ok)
		require.Len(t, postsRelations, 1)

		// 验证 user_id 外键
		userIdFK, ok := postsRelations["user_id"]
		require.True(t, ok)
		require.Equal(t, "users", userIdFK.TableName)
		require.Equal(t, "id", userIdFK.ColumnName)
		require.Equal(t, internal.MANY_TO_ONE, userIdFK.Kind)
	})
}
