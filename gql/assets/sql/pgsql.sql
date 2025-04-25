-- PostgreSQL版本的建表SQL

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

CREATE TABLE comments (
    id SERIAL PRIMARY KEY,
    content TEXT NOT NULL,
    user_id INTEGER NOT NULL REFERENCES users(id),
    post_id INTEGER NOT NULL REFERENCES posts(id),
    parent_id INTEGER REFERENCES comments(id),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE post_tags (
    post_id INTEGER NOT NULL REFERENCES posts(id),
    tag_id INTEGER NOT NULL REFERENCES tags(id),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (post_id, tag_id)
);

-- 设置表注释
COMMENT ON TABLE users IS '用户表';
COMMENT ON TABLE posts IS '文章表';
COMMENT ON TABLE tags IS '标签表';
COMMENT ON TABLE comments IS '评论表';
COMMENT ON TABLE post_tags IS '文章标签关联表';
-- 设置字段注释
COMMENT ON COLUMN users.name IS '用户名';
COMMENT ON COLUMN users.email IS '邮箱';
COMMENT ON COLUMN posts.title IS '标题';
COMMENT ON COLUMN posts.content IS '内容';
COMMENT ON COLUMN posts.user_id IS '作者ID';
COMMENT ON COLUMN tags.name IS '标签名称';
COMMENT ON COLUMN comments.content IS '评论内容';
COMMENT ON COLUMN comments.user_id IS '评论者';
COMMENT ON COLUMN comments.post_id IS '评论文章';
COMMENT ON COLUMN comments.parent_id IS '父评论ID';
COMMENT ON COLUMN post_tags.post_id IS '文章ID';
COMMENT ON COLUMN post_tags.tag_id IS '标签ID';