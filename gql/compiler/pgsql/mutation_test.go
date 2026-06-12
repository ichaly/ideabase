package pgsql

import (
	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/vektah/gqlparser/v2"
)

func (my *_DialectSuite) TestMutation() {
	cases := []Case{
		{
			name:  "创建实体",
			query: `mutation { createUser(input: { name: "Alice", email: "a@x.com" }) { id name } }`,
			args:  []any{"Alice", "a@x.com"},
			expected: `WITH "sys_user" AS (INSERT INTO sys_user ("name", "email") VALUES ($1, $2) RETURNING *)
				SELECT JSONB_BUILD_OBJECT('createUser', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT TO_JSONB("__sr_0".*) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id", "sys_user_0"."name" AS "name"
						FROM (SELECT "sys_user"."id", "sys_user"."name" FROM sys_user LIMIT 1) AS "sys_user_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:      "创建实体_整体input变量",
			query:     `mutation ($input: UserCreateInput!) { createUser(input: $input) { id } }`,
			variables: map[string]interface{}{"input": map[string]interface{}{"name": "Bob"}},
			args:      []any{"Bob"},
			expected: `WITH "sys_user" AS (INSERT INTO sys_user ("name") VALUES ($1) RETURNING *)
				SELECT JSONB_BUILD_OBJECT('createUser', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT TO_JSONB("__sr_0".*) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id"
						FROM (SELECT "sys_user"."id" FROM sys_user LIMIT 1) AS "sys_user_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "按id更新",
			query: `mutation { updateUser(input: { name: "New" }, id: 7) { id name } }`,
			args:  []any{"New", int64(7)},
			expected: `WITH "sys_user" AS (UPDATE sys_user SET "name" = $1 WHERE "sys_user"."id" = $2 RETURNING *)
				SELECT JSONB_BUILD_OBJECT('updateUser', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT TO_JSONB("__sr_0".*) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id", "sys_user_0"."name" AS "name"
						FROM (SELECT "sys_user"."id", "sys_user"."name" FROM sys_user LIMIT 1) AS "sys_user_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "按条件更新",
			query: `mutation { updateUser(input: { age: 20 }, where: { email: { like: "%@x.com" } }) { id } }`,
			args:  []any{int64(20), "%@x.com"},
			expected: `WITH "sys_user" AS (UPDATE sys_user SET "age" = $1 WHERE "sys_user"."email" LIKE $2 RETURNING *)
				SELECT JSONB_BUILD_OBJECT('updateUser', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT TO_JSONB("__sr_0".*) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id"
						FROM (SELECT "sys_user"."id" FROM sys_user LIMIT 1) AS "sys_user_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "按id删除返回计数",
			query: `mutation { deleteUser(id: 3) }`,
			args:  []any{int64(3)},
			expected: `WITH "sys_user" AS (DELETE FROM sys_user WHERE "sys_user"."id" = $1 RETURNING *)
				SELECT JSONB_BUILD_OBJECT('deleteUser', (SELECT COUNT(*) FROM "sys_user")) AS "__root" FROM (SELECT TRUE) AS "__root_x"`,
		},
		{
			name:  "创建后读回关系",
			query: `mutation { createPost(input: { title: "t", userId: 1 }) { id user { name } } }`,
			args:  []any{"t", int64(1)},
			expected: `WITH "sys_post" AS (INSERT INTO sys_post ("title", "user_id") VALUES ($1, $2) RETURNING *)
				SELECT JSONB_BUILD_OBJECT('createPost', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT TO_JSONB("__sr_0".*) AS "json"
					FROM (
						SELECT "sys_post_0"."id" AS "id", "__sj_1"."json" AS "user"
						FROM (SELECT "sys_post"."id", "sys_post"."user_id" FROM sys_post LIMIT 1) AS "sys_post_0"
						LEFT OUTER JOIN LATERAL (
							SELECT TO_JSONB("__sr_1".*) AS "json"
							FROM (
								SELECT "sys_user_1"."name" AS "name"
								FROM (SELECT "sys_user"."name" FROM sys_user WHERE "sys_user"."id" = "sys_post_0"."user_id" LIMIT 1) AS "sys_user_1"
							) AS "__sr_1"
						) AS "__sj_1" ON TRUE
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
	}
	my.runCases(cases)
}

// TestMutationGuards 变更安全防护：缺少条件的更新/删除直接报错
func (my *_DialectSuite) TestMutationGuards() {
	for name, query := range map[string]string{
		"无条件更新": `mutation { updateUser(input: { name: "x" }) { id } }`,
		"无条件删除": `mutation { deleteUser }`,
	} {
		my.Run(name, func() {
			doc, gqlErr := gqlparser.LoadQuery(my.schema, query)
			my.Require().Empty(gqlErr, "解析GraphQL查询失败")

			compile, err := gql.NewCompiler(my.meta, []compiler.Dialect{my.dialect})
			my.Require().NoError(err, "创建编译器失败")

			_, _, err = compile.Build(doc.Operations[0], nil)
			my.Assert().ErrorContains(err, "需要id或where条件")
		})
	}
}
