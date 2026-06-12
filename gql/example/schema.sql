-- gql引擎演示库：博客模型（用户/文章/标签/评论）
-- 覆盖四种关系：用户1-N文章、文章N-N标签（中间表）、评论自关联递归

CREATE EXTENSION IF NOT EXISTS pg_trgm; -- contrib模块，中文搜索的trigram索引

CREATE TABLE users (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    email TEXT NOT NULL UNIQUE,
    profile JSONB, -- jsonb contains操作符演示
    created_at TIMESTAMP NOT NULL DEFAULT now()
);

CREATE TABLE posts (
    id SERIAL PRIMARY KEY,
    title TEXT NOT NULL,
    content TEXT,
    user_id INTEGER NOT NULL REFERENCES users(id),
    created_at TIMESTAMP NOT NULL DEFAULT now()
);

CREATE TABLE tags (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE post_tags (
    post_id INTEGER NOT NULL REFERENCES posts(id),
    tag_id INTEGER NOT NULL REFERENCES tags(id),
    PRIMARY KEY (post_id, tag_id)
);

CREATE TABLE comments (
    id SERIAL PRIMARY KEY,
    content TEXT NOT NULL,
    user_id INTEGER NOT NULL REFERENCES users(id),
    post_id INTEGER NOT NULL REFERENCES posts(id),
    parent_id INTEGER REFERENCES comments(id), -- 自关联：递归全树演示
    created_at TIMESTAMP NOT NULL DEFAULT now()
);

-- 性能索引：搜索列trigram、外键、递归父链
CREATE INDEX idx_posts_title_trgm ON posts USING gin (title gin_trgm_ops);
CREATE INDEX idx_posts_content_trgm ON posts USING gin (content gin_trgm_ops);
CREATE INDEX idx_posts_user ON posts(user_id);
CREATE INDEX idx_comments_parent ON comments(parent_id);
CREATE INDEX idx_users_profile ON users USING gin (profile);

-- 种子数据（含中文，演示搜索）
INSERT INTO users (name, email, profile) VALUES
  ('张三', 'zhang@demo.dev', '{"vip": true, "city": "北京"}'),
  ('李四', 'li@demo.dev', '{"vip": false, "city": "上海"}'),
  ('王五', 'wang@demo.dev', NULL);

INSERT INTO posts (title, content, user_id) VALUES
  ('PostgreSQL数据库引擎选型', '对比各类全文检索方案的优劣', 1),
  ('Go语言工程实践', '数据库连接池与并发调优', 1),
  ('GraphQL接口设计', '单条SQL消除N+1的编译思路', 2),
  ('前端构建提速', '与后端无关的内容', 3);

INSERT INTO tags (name) VALUES ('数据库'), ('Go'), ('GraphQL'), ('性能');

INSERT INTO post_tags (post_id, tag_id) VALUES (1,1),(1,4),(2,2),(2,4),(3,3),(3,4);

INSERT INTO comments (content, user_id, post_id, parent_id) VALUES
  ('写得好', 2, 1, NULL),       -- id=1 根评论
  ('同意楼上', 3, 1, 1),        -- id=2 二层
  ('补充一点索引建议', 1, 1, 2); -- id=3 三层
