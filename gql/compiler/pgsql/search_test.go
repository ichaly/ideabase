package pgsql

import (
	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/vektah/gqlparser/v2"
)

func (my *_DialectSuite) TestSearch() {
	cases := []Case{
		{
			name:  "trigram搜索默认按相关度排序",
			query: `query { users(search: "数据库", limit: 5) { items { id name } } }`,
			args:  []any{"数据库", "数据库", "数据库", "数据库"},
			expected: `SELECT JSONB_BUILD_OBJECT('users', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]')) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id", "sys_user_0"."name" AS "name"
						FROM (SELECT "sys_user"."id", "sys_user"."name" FROM sys_user
							WHERE ("sys_user"."name" ILIKE '%' || $1 || '%' OR "sys_user"."email" ILIKE '%' || $2 || '%')
							ORDER BY GREATEST(similarity("sys_user"."name", $3), similarity("sys_user"."email", $4)) DESC
							LIMIT 5) AS "sys_user_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "搜索与条件排序组合",
			query: `query { users(search: "abc", where: { age: { gt: 18 } }, sort: { name: ASC }) { items { id } } }`,
			args:  []any{"abc", "abc", int64(18)},
			expected: `SELECT JSONB_BUILD_OBJECT('users', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]')) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id"
						FROM (SELECT "sys_user"."id", "sys_user"."name" FROM sys_user
							WHERE ("sys_user"."name" ILIKE '%' || $1 || '%' OR "sys_user"."email" ILIKE '%' || $2 || '%')
								AND "sys_user"."age" > $3
							ORDER BY "sys_user"."name" ASC) AS "sys_user_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
	}
	my.runCases(cases)
}

// TestSearchTsvector 分词模式：独立元数据实例验证jieba配置形态
func (my *_DialectSuite) TestSearchTsvector() {
	meta, schema, dialect := my.newSuite(map[string]interface{}{
		"search.mode":   "tsvector",
		"search.config": "jiebacfg",
	})

	doc, gqlErr := gqlparser.LoadQuery(schema, `query { users(search: "全文检索") { items { id } } }`)
	my.Require().Empty(gqlErr)
	compile, err := gql.NewCompiler(meta, []compiler.Dialect{dialect})
	my.Require().NoError(err)
	sql, args, err := compile.Build(doc.Operations[0], nil)
	my.Require().NoError(err)

	my.Assert().Contains(sql, `to_tsvector('jiebacfg', "sys_user"."name") @@ websearch_to_tsquery('jiebacfg', $1)`)
	my.Assert().Contains(sql, `ts_rank`)
	my.Assert().Equal([]any{"全文检索", "全文检索"}, args)
}

// TestSearchGuards 搜索约束
func (my *_DialectSuite) TestSearchGuards() {
	doc, gqlErr := gqlparser.LoadQuery(my.schema, `query { users(search: "x", first: 2) { items { id } } }`)
	my.Require().Empty(gqlErr)
	compile, err := gql.NewCompiler(my.meta, []compiler.Dialect{my.dialect})
	my.Require().NoError(err)
	_, _, err = compile.Build(doc.Operations[0], nil)
	my.Assert().ErrorContains(err, "必须显式sort")
}

// TestDistinctAndJsonb distinct去重与jsonb包含操作符
func (my *_DialectSuite) TestDistinctAndJsonb() {
	cases := []Case{
		{
			name:  "distinct去重并前置排序",
			query: `query { users(distinct: ["name"], sort: { age: DESC }) { items { id name } } }`,
			expected: `SELECT JSONB_BUILD_OBJECT('users', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]')) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id", "sys_user_0"."name" AS "name"
						FROM (SELECT DISTINCT ON ("sys_user"."name") "sys_user"."id", "sys_user"."name", "sys_user"."age" FROM sys_user
							ORDER BY "sys_user"."name", "sys_user"."age" DESC) AS "sys_user_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "jsonb包含操作符",
			query: `query { users(where: { metadata: { contains: { vip: true } } }) { items { id } } }`,
			args:  []any{`{"vip":true}`},
			expected: `SELECT JSONB_BUILD_OBJECT('users', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]')) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id"
						FROM (SELECT "sys_user"."id" FROM sys_user WHERE jsonb_contains("sys_user"."metadata", $1::jsonb)) AS "sys_user_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
	}
	my.runCases(cases)
}

// TestRecursiveTree 深度递归全树遍历
func (my *_DialectSuite) TestRecursiveTree() {
	cases := []Case{
		{
			name:  "全部后代默认限深5",
			query: `query { comments { items { id descendants { id content } } } }`,
			expected: `SELECT JSONB_BUILD_OBJECT('comments', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]')) AS "json"
					FROM (
						SELECT "sys_comment_0"."id" AS "id", "__sj_1"."json" AS "descendants"
						FROM (SELECT "sys_comment"."id" FROM sys_comment) AS "sys_comment_0"
						LEFT OUTER JOIN LATERAL (
							SELECT COALESCE(JSONB_AGG(TO_JSONB("__sr_1".*)), '[]') AS "json"
							FROM (
								SELECT "sys_comment_1"."id" AS "id", "sys_comment_1"."content" AS "content"
								FROM (WITH RECURSIVE "__tree_1" AS (
									SELECT "sys_comment"."id", "sys_comment"."content", "sys_comment"."parent_id", 1 AS "__lv"
									FROM sys_comment WHERE "sys_comment"."parent_id" = "sys_comment_0"."id"
									UNION ALL
									SELECT "sys_comment"."id", "sys_comment"."content", "sys_comment"."parent_id", "__tree_1"."__lv" + 1
									FROM sys_comment, "__tree_1"
									WHERE "sys_comment"."parent_id" = "__tree_1"."id" AND "__tree_1"."__lv" < 5
								) SELECT "__tree_1"."id", "__tree_1"."content", "__tree_1"."parent_id" FROM "__tree_1") AS "sys_comment_1"
							) AS "__sr_1"
						) AS "__sj_1" ON TRUE
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "祖先链限深2",
			query: `query { comments { items { id ancestors(depth: 2) { id } } } }`,
			expected: `SELECT JSONB_BUILD_OBJECT('comments', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]')) AS "json"
					FROM (
						SELECT "sys_comment_0"."id" AS "id", "__sj_1"."json" AS "ancestors"
						FROM (SELECT "sys_comment"."id", "sys_comment"."parent_id" FROM sys_comment) AS "sys_comment_0"
						LEFT OUTER JOIN LATERAL (
							SELECT COALESCE(JSONB_AGG(TO_JSONB("__sr_1".*)), '[]') AS "json"
							FROM (
								SELECT "sys_comment_1"."id" AS "id"
								FROM (WITH RECURSIVE "__tree_1" AS (
									SELECT "sys_comment"."id", "sys_comment"."parent_id", 1 AS "__lv"
									FROM sys_comment WHERE "sys_comment"."id" = "sys_comment_0"."parent_id"
									UNION ALL
									SELECT "sys_comment"."id", "sys_comment"."parent_id", "__tree_1"."__lv" + 1
									FROM sys_comment, "__tree_1"
									WHERE "sys_comment"."id" = "__tree_1"."parent_id" AND "__tree_1"."__lv" < 2
								) SELECT "__tree_1"."id", "__tree_1"."parent_id" FROM "__tree_1") AS "sys_comment_1"
							) AS "__sr_1"
						) AS "__sj_1" ON TRUE
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
	}
	my.runCases(cases)
}
