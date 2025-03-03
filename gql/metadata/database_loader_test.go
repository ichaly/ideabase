package metadata

import (
	"context"
	"fmt"
	"testing"
	"time"

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
	if db.Dialector.Name() == "mysql" {
		// MySQL 需要分别执行每个语句
		sqlStatements := []string{
			`CREATE TABLE users (
				id BIGINT AUTO_INCREMENT PRIMARY KEY,
				name VARCHAR(255) NOT NULL,
				email VARCHAR(255) UNIQUE NOT NULL,
				created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
			) COMMENT='用户表'`,

			`CREATE TABLE posts (
				id BIGINT AUTO_INCREMENT PRIMARY KEY,
				title VARCHAR(255) NOT NULL COMMENT '标题',
				content TEXT COMMENT '内容',
				user_id BIGINT,
				created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (user_id) REFERENCES users(id)
			) COMMENT='文章表'`,

			`CREATE TABLE comments (
				id BIGINT AUTO_INCREMENT PRIMARY KEY,
				content TEXT NOT NULL COMMENT '评论内容',
				user_id BIGINT NOT NULL COMMENT '评论者',
				post_id BIGINT NOT NULL COMMENT '评论文章',
				parent_id BIGINT COMMENT '父评论ID',
				created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (user_id) REFERENCES users(id),
				FOREIGN KEY (post_id) REFERENCES posts(id),
				FOREIGN KEY (parent_id) REFERENCES comments(id)
			) COMMENT='评论表'`,

			`CREATE TABLE tags (
				id BIGINT AUTO_INCREMENT PRIMARY KEY,
				name VARCHAR(50) NOT NULL UNIQUE COMMENT '标签名称',
				description TEXT COMMENT '标签描述',
				created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
			) COMMENT='标签表'`,

			`CREATE TABLE post_tags (
				post_id BIGINT NOT NULL COMMENT '文章ID',
				tag_id BIGINT NOT NULL COMMENT '标签ID',
				created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
				PRIMARY KEY (post_id, tag_id),
				FOREIGN KEY (post_id) REFERENCES posts(id),
				FOREIGN KEY (tag_id) REFERENCES tags(id)
			) COMMENT='文章标签关联表'`,

			`ALTER TABLE users MODIFY COLUMN name VARCHAR(255) NOT NULL COMMENT '用户名'`,
			`ALTER TABLE users MODIFY COLUMN email VARCHAR(255) NOT NULL COMMENT '邮箱'`,
			`ALTER TABLE posts MODIFY COLUMN user_id BIGINT COMMENT '作者ID'`,
		}

		// 分别执行每个SQL语句
		for _, stmt := range sqlStatements {
			if err := db.Exec(stmt).Error; err != nil {
				require.NoError(t, err)
			}
		}
	} else {
		// PostgreSQL 可以一次执行多个语句
		createTableSQL := `
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

			CREATE TABLE comments (
				id SERIAL PRIMARY KEY,
				content TEXT NOT NULL,
				user_id INTEGER NOT NULL REFERENCES users(id),
				post_id INTEGER NOT NULL REFERENCES posts(id),
				parent_id INTEGER REFERENCES comments(id),
				created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
			);

			CREATE TABLE tags (
				id SERIAL PRIMARY KEY,
				name VARCHAR(50) NOT NULL UNIQUE,
				description TEXT,
				created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
			);

			CREATE TABLE post_tags (
				post_id INTEGER NOT NULL REFERENCES posts(id),
				tag_id INTEGER NOT NULL REFERENCES tags(id),
				created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
				PRIMARY KEY (post_id, tag_id)
			);

			COMMENT ON TABLE users IS '用户表';
			COMMENT ON COLUMN users.name IS '用户名';
			COMMENT ON COLUMN users.email IS '邮箱';

			COMMENT ON TABLE posts IS '文章表';
			COMMENT ON COLUMN posts.title IS '标题';
			COMMENT ON COLUMN posts.content IS '内容';
			COMMENT ON COLUMN posts.user_id IS '作者ID';

			COMMENT ON TABLE comments IS '评论表';
			COMMENT ON COLUMN comments.content IS '评论内容';
			COMMENT ON COLUMN comments.user_id IS '评论者';
			COMMENT ON COLUMN comments.post_id IS '评论文章';
			COMMENT ON COLUMN comments.parent_id IS '父评论ID';

			COMMENT ON TABLE tags IS '标签表';
			COMMENT ON COLUMN tags.name IS '标签名称';
			COMMENT ON COLUMN tags.description IS '标签描述';

			COMMENT ON TABLE post_tags IS '文章标签关联表';
			COMMENT ON COLUMN post_tags.post_id IS '文章ID';
			COMMENT ON COLUMN post_tags.tag_id IS '标签ID';
		`

		err := db.Exec(createTableSQL).Error
		require.NoError(t, err)
	}

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
		require.Len(t, classes, 5) // 现在应该有5个类：users, posts, comments, tags, post_tags

		// 验证users表
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

		// 验证tags表
		tags, ok := classes["tags"]
		require.True(t, ok)
		require.Equal(t, "tags", tags.Table)
		require.Equal(t, "标签表", tags.Description)
		require.Len(t, tags.Fields, 4)
		require.Contains(t, tags.Fields, "id")
		require.Contains(t, tags.Fields, "name")
		require.Contains(t, tags.Fields, "description")
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

	// 验证关系信息
	t.Run("验证关系信息", func(t *testing.T) {
		// 验证posts表的关系
		postsRelations, ok := relationships["posts"]
		require.True(t, ok)
		require.Len(t, postsRelations, 1)
		userIdFK, ok := postsRelations["user_id"]
		require.True(t, ok)
		require.Equal(t, "users", userIdFK.TargetClass)
		require.Equal(t, "id", userIdFK.TargetField)

		// 验证comments表的关系
		commentsRelations, ok := relationships["comments"]
		require.True(t, ok)
		require.Len(t, commentsRelations, 3)

		// 验证评论与用户的关系
		commentUserFK, ok := commentsRelations["user_id"]
		require.True(t, ok)
		require.Equal(t, "users", commentUserFK.TargetClass)
		require.Equal(t, "id", commentUserFK.TargetField)

		// 验证评论与文章的关系
		commentPostFK, ok := commentsRelations["post_id"]
		require.True(t, ok)
		require.Equal(t, "posts", commentPostFK.TargetClass)
		require.Equal(t, "id", commentPostFK.TargetField)

		// 验证评论的自关联
		commentParentFK, ok := commentsRelations["parent_id"]
		require.True(t, ok)
		require.Equal(t, "comments", commentParentFK.TargetClass)
		require.Equal(t, "id", commentParentFK.TargetField)

		// 验证post_tags表的关系
		postTagsRelations, ok := relationships["post_tags"]
		require.True(t, ok)
		require.Len(t, postTagsRelations, 2)

		// 验证post_tags与posts的关系
		postTagPostFK, ok := postTagsRelations["post_id"]
		require.True(t, ok)
		require.Equal(t, "posts", postTagPostFK.TargetClass)
		require.Equal(t, "id", postTagPostFK.TargetField)

		// 验证post_tags与tags的关系
		postTagTagFK, ok := postTagsRelations["tag_id"]
		require.True(t, ok)
		require.Equal(t, "tags", postTagTagFK.TargetClass)
		require.Equal(t, "id", postTagTagFK.TargetField)
	})
}
