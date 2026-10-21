package sql

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTags(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{
			name: "普通查询",
			sql:  `SELECT * FROM users WHERE id = 1`,
			want: []string{"users"},
		},
		{
			name: "带别名",
			sql:  `SELECT * FROM cms_channel_lineage cl JOIN cms_lineage l ON l.id = cl.lineage_id`,
			want: []string{"cms_channel_lineage", "cms_lineage"},
		},
		{
			name: "子查询",
			sql:  `SELECT * FROM (SELECT id FROM cms_lineage WHERE state = 1) sub`,
			want: []string{"cms_lineage"},
		},
		{
			name: "CTE",
			sql:  `WITH cte AS (SELECT id FROM cms_lineage) SELECT * FROM cte`,
			want: []string{"cms_lineage"},
		},
		{
			name: "CTE 加 JOIN",
			sql:  `WITH cte AS (SELECT id FROM cms_lineage) SELECT * FROM cte JOIN cms_channel c ON c.id = 1`,
			want: []string{"cms_lineage", "cms_channel"},
		},
		{
			name: "嵌套子查询",
			sql:  `SELECT * FROM (SELECT * FROM (SELECT id FROM orders) t1) t2`,
			want: []string{"orders"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, cacheable := Tags("postgres", c.sql)
			require.True(t, cacheable)
			require.ElementsMatch(t, c.want, got)
		})
	}
}

func TestTagsSafetyAndScopes(t *testing.T) {
	tests := []struct {
		name, dialect, sql string
		want               []string
		cacheable          bool
	}{
		{"PG CTE same name keeps physical table", "postgres", `WITH users AS (SELECT * FROM users) SELECT * FROM users`, []string{"users"}, true},
		{"PG quoted case", "postgres", `SELECT * FROM public."Users"`, []string{"Users", `public.Users`}, true},
		{"PG locking bypass", "postgres", `SELECT * FROM users FOR UPDATE`, nil, false},
		{"PG DML bypass", "postgres", `UPDATE users SET name = 'x'`, nil, false},
		{"MySQL join", "mysql", "SELECT * FROM db.users u JOIN orders o ON o.user_id = u.id", []string{"db.users", "orders", "users"}, true},
		{"MySQL identifier case tags", "mysql", "SELECT * FROM `App`.`Users`", []string{"app.users", "users"}, true},
		{"MySQL union", "mysql", "SELECT * FROM users UNION ALL SELECT * FROM archived_users", []string{"archived_users", "users"}, true},
		{"MySQL self named CTE", "mysql", "WITH users AS (SELECT * FROM users) SELECT * FROM users", []string{"users"}, true},
		{"MySQL nested CTE shadow", "mysql", "SELECT * FROM recent WHERE EXISTS (WITH recent AS (SELECT * FROM archive.posts) SELECT 1 FROM recent)", []string{"recent", "archive.posts", "posts"}, true},
		{"MySQL qualified CTE name", "mysql", "WITH users AS (SELECT * FROM staging.users) SELECT * FROM users JOIN public.users p ON true", []string{"users", "staging.users", "public.users"}, true},
		{"MySQL outfile bypass", "mysql", "SELECT * FROM users INTO OUTFILE '/tmp/output'", nil, false},
		{"MySQL recursive CTE", "mysql", "WITH RECURSIVE tree AS (SELECT id FROM categories UNION ALL SELECT c.id FROM categories c JOIN tree t ON c.parent_id=t.id) SELECT * FROM tree", []string{"categories"}, true},
		{"MySQL CTE union", "mysql", "WITH selected AS (SELECT * FROM users) SELECT id FROM selected UNION ALL SELECT id FROM archived_users", []string{"users", "archived_users"}, true},
		{"MySQL CTE case insensitive", "mysql", "WITH Selected AS (SELECT * FROM users) SELECT * FROM selected", []string{"users"}, true},
		{"MySQL strings and comments", "mysql", "SELECT 'FROM fake' FROM `users` /* JOIN ignored */ WHERE id = ?", []string{"users"}, true},
		{"MySQL nested locking bypass", "mysql", "SELECT * FROM (SELECT * FROM users FOR UPDATE) u", nil, false},
		{"MySQL invalid bypass", "mysql", "SELECT * FROM", nil, false},
		{"MySQL multiple bypass", "mysql", "SELECT * FROM users; SELECT * FROM orders", nil, false},
		{"PG recursive", "postgres", `WITH RECURSIVE tree AS (SELECT id FROM categories UNION ALL SELECT c.id FROM categories c JOIN tree t ON c.parent_id=t.id) SELECT * FROM tree JOIN products p ON p.category_id=tree.id`, []string{"categories", "products"}, true},
		{"PG writable CTE bypass", "postgres", `WITH gone AS (DELETE FROM users RETURNING *) SELECT * FROM gone`, nil, false},
		{"PG sampling", "postgres", `SELECT * FROM ONLY public.events TABLESAMPLE SYSTEM (10) REPEATABLE (42)`, []string{"events", "public.events"}, true},
		{"PG nested CTE shadow", "postgres", `SELECT * FROM recent WHERE EXISTS (WITH recent AS (SELECT * FROM archive.posts) SELECT 1 FROM recent)`, []string{"recent", "archive.posts", "posts"}, true},
		{"PG CTE forward physical name", "postgres", `WITH a AS (SELECT * FROM b), b AS (SELECT * FROM c) SELECT * FROM a,b`, []string{"b", "c"}, true},
		{"PG quoted CTE distinct case", "postgres", `WITH "Users" AS (SELECT * FROM people) SELECT * FROM users JOIN "Users" u ON true`, []string{"people", "users"}, true},
		{"PG lateral scalar subqueries", "postgres", `SELECT (SELECT count(*) FROM comments) FROM users u LEFT JOIN LATERAL (SELECT * FROM orders o WHERE o.user_id=u.id LIMIT 1) latest ON true`, []string{"comments", "users", "orders"}, true},
		{"PG union", "postgres", `SELECT id FROM users UNION SELECT id FROM archived_users EXCEPT SELECT id FROM blocked_users`, []string{"users", "archived_users", "blocked_users"}, true},
		{"PG strings and comments", "postgres", `SELECT 'FROM fake', $$JOIN fake$$ FROM users /* JOIN ignored */ WHERE id=$1`, []string{"users"}, true},
		{"PG into bypass", "postgres", `SELECT * INTO saved_users FROM users`, nil, false},
		{"PG multiple bypass", "postgres", `SELECT * FROM users; SELECT * FROM orders`, nil, false},
		{"invalid bypass", "postgres", `SELECT * FROM`, nil, false},
		{"SQLite bypass", "sqlite", "SELECT * FROM users", nil, false},
		{"unknown dialect bypass", "unknown", "SELECT * FROM users", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := Tags(tt.dialect, tt.sql)
			require.Equal(t, tt.cacheable, ok)
			if ok {
				require.ElementsMatch(t, tt.want, got)
			}
		})
	}
}
