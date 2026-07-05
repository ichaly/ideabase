package pgsql

import (
	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/vektah/gqlparser/v2"
)

func (my *_DialectSuite) TestSelect() {
	cases := []Case{
		{
			name:  "基础查询",
			query: `query { users { items { id name email } } }`,
			expected: `SELECT JSONB_BUILD_OBJECT('users', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]')) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id", "sys_user_0"."name" AS "name", "sys_user_0"."email" AS "email"
						FROM (SELECT "sys_user"."id", "sys_user"."name", "sys_user"."email" FROM "public"."sys_user" LIMIT 10) AS "sys_user_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "id参数查询",
			query: `query { users(id: 1) { items { id name } } }`,
			args:  []any{int64(1)},
			expected: `SELECT JSONB_BUILD_OBJECT('users', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]')) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id", "sys_user_0"."name" AS "name"
						FROM (SELECT "sys_user"."id", "sys_user"."name" FROM "public"."sys_user" WHERE "sys_user"."id" = $1 LIMIT 10) AS "sys_user_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "分页与total统计",
			query: `query { users(limit: 10, offset: 20) { items { id } total } }`,
			expected: `SELECT JSONB_BUILD_OBJECT('users', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*) - '__total'), '[]'), 'total', COALESCE(MIN("__sr_0"."__total"), 0)) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id", "sys_user_0"."__total"
						FROM (SELECT "sys_user"."id", COUNT(*) OVER() AS "__total" FROM "public"."sys_user" LIMIT 10 OFFSET 20) AS "sys_user_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "字段别名",
			query: `query { users { items { uid: id userName: name } } }`,
			expected: `SELECT JSONB_BUILD_OBJECT('users', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]')) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "uid", "sys_user_0"."name" AS "userName"
						FROM (SELECT "sys_user"."id", "sys_user"."name" FROM "public"."sys_user" LIMIT 10) AS "sys_user_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "__typename元字段",
			query: `query { __typename users { __typename items { id __typename posts { __typename title } } } }`,
			expected: `SELECT JSONB_BUILD_OBJECT('__typename', 'Query', 'users', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]'), '__typename', 'UserResult') AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id", 'User' AS "__typename", "__sj_1"."json" AS "posts"
						FROM (SELECT "sys_user"."id" FROM "public"."sys_user" LIMIT 10) AS "sys_user_0"
						LEFT OUTER JOIN LATERAL (
							SELECT COALESCE(JSONB_AGG(TO_JSONB("__sr_1".*)), '[]') AS "json"
							FROM (
								SELECT "sys_post_1"."title" AS "title", 'Post' AS "__typename"
								FROM (SELECT "sys_post"."title" FROM "public"."sys_post" WHERE "sys_post"."user_id" = "sys_user_0"."id" LIMIT 10) AS "sys_post_1"
							) AS "__sr_1"
						) AS "__sj_1" ON TRUE
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "多根字段查询",
			query: `query { users { items { id } } tags { items { name } } }`,
			expected: `SELECT JSONB_BUILD_OBJECT('users', "__sj_0"."json", 'tags', "__sj_1"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]')) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id"
						FROM (SELECT "sys_user"."id" FROM "public"."sys_user" LIMIT 10) AS "sys_user_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_1".*)), '[]')) AS "json"
					FROM (
						SELECT "sys_tag_1"."name" AS "name"
						FROM (SELECT "sys_tag"."name" FROM "public"."sys_tag" LIMIT 10) AS "sys_tag_1"
					) AS "__sr_1"
				) AS "__sj_1" ON TRUE`,
		},
	}
	my.runCases(cases)
}

func (my *_DialectSuite) TestRelation() {
	cases := []Case{
		{
			name:  "一对多关系",
			query: `query { users { items { id posts { title } } } }`,
			expected: `SELECT JSONB_BUILD_OBJECT('users', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]')) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id", "__sj_1"."json" AS "posts"
						FROM (SELECT "sys_user"."id" FROM "public"."sys_user" LIMIT 10) AS "sys_user_0"
						LEFT OUTER JOIN LATERAL (
							SELECT COALESCE(JSONB_AGG(TO_JSONB("__sr_1".*)), '[]') AS "json"
							FROM (
								SELECT "sys_post_1"."title" AS "title"
								FROM (SELECT "sys_post"."title" FROM "public"."sys_post" WHERE "sys_post"."user_id" = "sys_user_0"."id" LIMIT 10) AS "sys_post_1"
							) AS "__sr_1"
						) AS "__sj_1" ON TRUE
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "多对一关系",
			query: `query { posts { items { title user { name } } } }`,
			expected: `SELECT JSONB_BUILD_OBJECT('posts', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]')) AS "json"
					FROM (
						SELECT "sys_post_0"."title" AS "title", "__sj_1"."json" AS "user"
						FROM (SELECT "sys_post"."title", "sys_post"."user_id" FROM "public"."sys_post" LIMIT 10) AS "sys_post_0"
						LEFT OUTER JOIN LATERAL (
							SELECT TO_JSONB("__sr_1".*) AS "json"
							FROM (
								SELECT "sys_user_1"."name" AS "name"
								FROM (SELECT "sys_user"."name" FROM "public"."sys_user" WHERE "sys_user"."id" = "sys_post_0"."user_id" LIMIT 1) AS "sys_user_1"
							) AS "__sr_1"
						) AS "__sj_1" ON TRUE
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "多对多关系",
			query: `query { posts { items { id tags { name } } } }`,
			expected: `SELECT JSONB_BUILD_OBJECT('posts', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]')) AS "json"
					FROM (
						SELECT "sys_post_0"."id" AS "id", "__sj_1"."json" AS "tags"
						FROM (SELECT "sys_post"."id" FROM "public"."sys_post" LIMIT 10) AS "sys_post_0"
						LEFT OUTER JOIN LATERAL (
							SELECT COALESCE(JSONB_AGG(TO_JSONB("__sr_1".*)), '[]') AS "json"
							FROM (
								SELECT "sys_tag_1"."name" AS "name"
								FROM (
									SELECT "sys_tag"."name" FROM "public"."sys_tag"
									INNER JOIN "public"."sys_post_tag" ON "sys_post_tag"."tag_id" = "sys_tag"."id"
									WHERE "sys_post_tag"."post_id" = "sys_post_0"."id"
								 LIMIT 10) AS "sys_tag_1"
							) AS "__sr_1"
						) AS "__sj_1" ON TRUE
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "递归关系",
			query: `query { comments { items { id children { id } parent { id } } } }`,
			expected: `SELECT JSONB_BUILD_OBJECT('comments', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]')) AS "json"
					FROM (
						SELECT "sys_comment_0"."id" AS "id", "__sj_1"."json" AS "children", "__sj_2"."json" AS "parent"
						FROM (SELECT "sys_comment"."id", "sys_comment"."parent_id" FROM "public"."sys_comment" LIMIT 10) AS "sys_comment_0"
						LEFT OUTER JOIN LATERAL (
							SELECT COALESCE(JSONB_AGG(TO_JSONB("__sr_1".*)), '[]') AS "json"
							FROM (
								SELECT "sys_comment_1"."id" AS "id"
								FROM (SELECT "sys_comment"."id" FROM "public"."sys_comment" WHERE "sys_comment"."parent_id" = "sys_comment_0"."id" LIMIT 10) AS "sys_comment_1"
							) AS "__sr_1"
						) AS "__sj_1" ON TRUE
						LEFT OUTER JOIN LATERAL (
							SELECT TO_JSONB("__sr_2".*) AS "json"
							FROM (
								SELECT "sys_comment_2"."id" AS "id"
								FROM (SELECT "sys_comment"."id" FROM "public"."sys_comment" WHERE "sys_comment"."id" = "sys_comment_0"."parent_id" LIMIT 1) AS "sys_comment_2"
							) AS "__sr_2"
						) AS "__sj_2" ON TRUE
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "嵌套关系参数(过滤排序分页)",
			query: `query { users { items { id posts(where: { title: { like: "%a%" } }, sort: { title: DESC }, limit: 3) { title } } } }`,
			args:  []any{"%a%"},
			expected: `SELECT JSONB_BUILD_OBJECT('users', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]')) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id", "__sj_1"."json" AS "posts"
						FROM (SELECT "sys_user"."id" FROM "public"."sys_user" LIMIT 10) AS "sys_user_0"
						LEFT OUTER JOIN LATERAL (
							SELECT COALESCE(JSONB_AGG(TO_JSONB("__sr_1".*)), '[]') AS "json"
							FROM (
								SELECT "sys_post_1"."title" AS "title"
								FROM (SELECT "sys_post"."title" FROM "public"."sys_post"
									WHERE "sys_post"."user_id" = "sys_user_0"."id" AND "sys_post"."title" LIKE $1
									ORDER BY "sys_post"."title" DESC LIMIT 3) AS "sys_post_1"
							) AS "__sr_1"
						) AS "__sj_1" ON TRUE
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "嵌套两层关系",
			query: `query { users { items { id posts { title tags { name } } } } }`,
			expected: `SELECT JSONB_BUILD_OBJECT('users', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]')) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id", "__sj_1"."json" AS "posts"
						FROM (SELECT "sys_user"."id" FROM "public"."sys_user" LIMIT 10) AS "sys_user_0"
						LEFT OUTER JOIN LATERAL (
							SELECT COALESCE(JSONB_AGG(TO_JSONB("__sr_1".*)), '[]') AS "json"
							FROM (
								SELECT "sys_post_1"."title" AS "title", "__sj_2"."json" AS "tags"
								FROM (SELECT "sys_post"."title", "sys_post"."id" FROM "public"."sys_post" WHERE "sys_post"."user_id" = "sys_user_0"."id" LIMIT 10) AS "sys_post_1"
								LEFT OUTER JOIN LATERAL (
									SELECT COALESCE(JSONB_AGG(TO_JSONB("__sr_2".*)), '[]') AS "json"
									FROM (
										SELECT "sys_tag_2"."name" AS "name"
										FROM (
											SELECT "sys_tag"."name" FROM "public"."sys_tag"
											INNER JOIN "public"."sys_post_tag" ON "sys_post_tag"."tag_id" = "sys_tag"."id"
											WHERE "sys_post_tag"."post_id" = "sys_post_1"."id"
										 LIMIT 10) AS "sys_tag_2"
									) AS "__sr_2"
								) AS "__sj_2" ON TRUE
							) AS "__sr_1"
						) AS "__sj_1" ON TRUE
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
	}
	my.runCases(cases)
}

// TestSelectGuards 列表查询参数约束:distinct去重后行数与COUNT(*) OVER()窗口
// 计数(去重前求值)语义冲突,须编译期拒绝而非静默返回错误total
func (my *_DialectSuite) TestSelectGuards() {
	doc, gqlErr := gqlparser.LoadQuery(my.schema, `query { users(distinct: ["name"]) { items { id } total } }`)
	my.Require().Empty(gqlErr, "解析失败")

	compile, err := gql.NewCompiler(my.meta, []compiler.Dialect{my.dialect})
	my.Require().NoError(err)
	_, _, err = compile.Build(doc.Operations[0], nil)
	my.Assert().ErrorContains(err, "distinct与total不能同时使用")
}

// TestDepthGuard 查询深度护栏:嵌套LATERAL单元无上限时,深选择集可编译出
// 巨大SQL树(代价攻击面),须编译期按schema.max-depth拒绝
func (my *_DialectSuite) TestDepthGuard() {
	meta, schema, dialect := my.newSuite(map[string]interface{}{"schema.max-depth": 2})
	compile, err := gql.NewCompiler(meta, []compiler.Dialect{dialect})
	my.Require().NoError(err)

	// users>items>id 深度3,超过上限2
	doc, gqlErr := gqlparser.LoadQuery(schema, `query { users { items { id } } }`)
	my.Require().Empty(gqlErr)
	_, _, err = compile.Build(doc.Operations[0], nil)
	my.Assert().ErrorContains(err, "深度")

	// total 只有2层,不超
	doc, gqlErr = gqlparser.LoadQuery(schema, `query { users { total } }`)
	my.Require().Empty(gqlErr)
	_, _, err = compile.Build(doc.Operations[0], nil)
	my.Assert().NoError(err)

	// 缺省上限(20)不影响常规嵌套查询(主套件全部用例即回归)
}
