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

// TestRelationOps 嵌套写入：connect/disconnect 编译为关系操作CTE
func (my *_DialectSuite) TestRelationOps() {
	cases := []Case{
		{
			name:  "创建并挂接一对多",
			query: `mutation { createUser(input: { name: "A", email: "a@x.com", posts: { connect: [1, 2] } }) { id } }`,
			args:  []any{"A", "a@x.com", int64(1), int64(2)},
			expected: `WITH "sys_user" AS (INSERT INTO sys_user ("name", "email") VALUES ($1, $2) RETURNING *),
				"__c_1" AS (UPDATE sys_post SET "user_id" = (SELECT "id" FROM "sys_user") WHERE "id" IN ($3, $4))
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
			name:  "更新多对多挂接与解除",
			query: `mutation { updatePost(input: { tags: { connect: [10], disconnect: [11] } }, id: 5) { id } }`,
			args:  []any{int64(5), int64(10), int64(11)},
			expected: `WITH "sys_post" AS (SELECT * FROM sys_post WHERE "sys_post"."id" = $1),
				"__c_1" AS (INSERT INTO sys_post_tag ("post_id", "tag_id") VALUES ((SELECT "id" FROM "sys_post"), $2)),
				"__c_2" AS (DELETE FROM sys_post_tag WHERE "post_id" = (SELECT "id" FROM "sys_post") AND "tag_id" IN ($3))
				SELECT JSONB_BUILD_OBJECT('updatePost', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT TO_JSONB("__sr_0".*) AS "json"
					FROM (
						SELECT "sys_post_0"."id" AS "id"
						FROM (SELECT "sys_post"."id" FROM sys_post LIMIT 1) AS "sys_post_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
	}
	my.runCases(cases)
}

// TestRelationOpGuards 关系操作约束
func (my *_DialectSuite) TestRelationOpGuards() {
	for name, c := range map[string]struct{ query, wants string }{
		"创建不支持disconnect": {`mutation { createUser(input: { name: "x", email: "e", posts: { disconnect: [1] } }) { id } }`, "不支持disconnect"},
		"关系操作必须按id":       {`mutation { updateUser(input: { posts: { connect: [1] } }, where: { name: { eq: "x" } }) { id } }`, "必须用id定位"},
	} {
		my.Run(name, func() {
			doc, gqlErr := gqlparser.LoadQuery(my.schema, c.query)
			my.Require().Empty(gqlErr, "解析失败")
			compile, err := gql.NewCompiler(my.meta, []compiler.Dialect{my.dialect})
			my.Require().NoError(err)
			_, _, err = compile.Build(doc.Operations[0], nil)
			my.Assert().ErrorContains(err, c.wants)
		})
	}
}

// TestBulkMutations 批量插入/upsert/嵌套创建
func (my *_DialectSuite) TestBulkMutations() {
	cases := []Case{
		{
			name:  "批量创建多行VALUES缺失列填DEFAULT",
			query: `mutation { createUsers(input: [{ name: "A", email: "a@x" }, { name: "B", email: "b@x", age: 3 }]) { id name } }`,
			args:  []any{"A", "a@x", "B", "b@x", int64(3)},
			expected: `WITH "sys_user" AS (INSERT INTO sys_user ("name", "email", "age") VALUES ($1, $2, DEFAULT), ($3, $4, $5) RETURNING *)
				SELECT JSONB_BUILD_OBJECT('createUsers', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]') AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id", "sys_user_0"."name" AS "name"
						FROM (SELECT "sys_user"."id", "sys_user"."name" FROM sys_user) AS "sys_user_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "upsert按邮箱冲突",
			query: `mutation { upsertUsers(input: [{ name: "A", email: "a@x" }], on: ["email"]) { id } }`,
			args:  []any{"A", "a@x"},
			expected: `WITH "sys_user" AS (INSERT INTO sys_user ("name", "email") VALUES ($1, $2)
					ON CONFLICT ("email") DO UPDATE SET "name" = EXCLUDED."name" RETURNING *)
				SELECT JSONB_BUILD_OBJECT('upsertUsers', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]') AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id"
						FROM (SELECT "sys_user"."id" FROM sys_user) AS "sys_user_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "创建并内联建子行",
			query: `mutation { createUser(input: { name: "A", email: "a@x", posts: { create: [{ title: "P1" }, { title: "P2" }] } }) { id } }`,
			args:  []any{"A", "a@x", "P1", "P2"},
			expected: `WITH "sys_user" AS (INSERT INTO sys_user ("name", "email") VALUES ($1, $2) RETURNING *),
				"__c_1" AS (INSERT INTO sys_post ("title", "user_id") VALUES ($3, (SELECT "id" FROM "sys_user")), ($4, (SELECT "id" FROM "sys_user")))
				SELECT JSONB_BUILD_OBJECT('createUser', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT TO_JSONB("__sr_0".*) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id"
						FROM (SELECT "sys_user"."id" FROM sys_user LIMIT 1) AS "sys_user_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
	}
	my.runCases(cases)
}
