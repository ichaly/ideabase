package pgsql

import (
	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/vektah/gqlparser/v2"
)

// 条件用例共用的SQL骨架：仅WHERE子句与参数不同
func userQuery(where string) string {
	return `SELECT JSONB_BUILD_OBJECT('users', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
		LEFT OUTER JOIN LATERAL (
			SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]')) AS "json"
			FROM (
				SELECT "sys_user_0"."id" AS "id"
				FROM (SELECT "sys_user"."id" FROM "public"."sys_user" ` + where + ` LIMIT 10) AS "sys_user_0"
			) AS "__sr_0"
		) AS "__sj_0" ON TRUE`
}

func (my *_DialectSuite) TestWhere() {
	cases := []Case{
		{
			name:     "等值条件",
			query:    `query { users(where: { name: { eq: "test" } }) { items { id } } }`,
			expected: userQuery(`WHERE "sys_user"."name" = $1`),
			args:     []any{"test"},
		},
		{
			name:     "模糊匹配",
			query:    `query { users(where: { name: { like: "%test%" } }) { items { id } } }`,
			expected: userQuery(`WHERE "sys_user"."name" LIKE $1`),
			args:     []any{"%test%"},
		},
		{
			name:     "忽略大小写匹配",
			query:    `query { users(where: { name: { iLike: "%test%" } }) { items { id } } }`,
			expected: userQuery(`WHERE "sys_user"."name" ILIKE $1`),
			args:     []any{"%test%"},
		},
		{
			name:     "IN列表",
			query:    `query { users(where: { id: { in: [1, 2, 3] } }) { items { id } } }`,
			expected: userQuery(`WHERE "sys_user"."id" IN ($1, $2, $3)`),
			args:     []any{int64(1), int64(2), int64(3)},
		},
		{
			name:     "NULL判断",
			query:    `query { users(where: { age: { is: NULL } }) { items { id } } }`,
			expected: userQuery(`WHERE "sys_user"."age" IS NULL`),
		},
		{
			name:     "非NULL判断",
			query:    `query { users(where: { age: { is: NOT_NULL } }) { items { id } } }`,
			expected: userQuery(`WHERE "sys_user"."age" IS NOT NULL`),
		},
		{
			name:     "同字段多操作符",
			query:    `query { users(where: { age: { gt: 18, le: 60 } }) { items { id } } }`,
			expected: userQuery(`WHERE "sys_user"."age" > $1 AND "sys_user"."age" <= $2`),
			args:     []any{int64(18), int64(60)},
		},
		{
			name:     "多字段AND组合",
			query:    `query { users(where: { name: { eq: "a" }, age: { gt: 18 } }) { items { id } } }`,
			expected: userQuery(`WHERE ("sys_user"."name" = $1 AND "sys_user"."age" > $2)`),
			args:     []any{"a", int64(18)},
		},
		{
			name:     "OR组合",
			query:    `query { users(where: { or: [{ name: { eq: "a" } }, { email: { like: "%b%" } }] }) { items { id } } }`,
			expected: userQuery(`WHERE ("sys_user"."name" = $1 OR "sys_user"."email" LIKE $2)`),
			args:     []any{"a", "%b%"},
		},
		{
			name:     "NOT取反",
			query:    `query { users(where: { not: { name: { eq: "a" } } }) { items { id } } }`,
			expected: userQuery(`WHERE NOT ("sys_user"."name" = $1)`),
			args:     []any{"a"},
		},
		{
			name:     "id与where组合",
			query:    `query { users(id: 1, where: { name: { like: "%t%" } }) { items { id } } }`,
			expected: userQuery(`WHERE ("sys_user"."id" = $1 AND "sys_user"."name" LIKE $2)`),
			args:     []any{int64(1), "%t%"},
		},
		{
			name:      "变量参数",
			query:     `query ($name: String) { users(where: { name: { eq: $name } }) { items { id } } }`,
			variables: map[string]interface{}{"name": "dynamic"},
			expected:  userQuery(`WHERE "sys_user"."name" = $1`),
			args:      []any{"dynamic"},
		},
	}
	my.runCases(cases)
}

// TestWherePruning 空对象条件剪枝：语义上空对象=无条件恒真，剪空后不输出WHERE/AND/NOT
func (my *_DialectSuite) TestWherePruning() {
	cases := []Case{
		{
			name:     "空where不输出WHERE",
			query:    `query { users(where: {}) { items { id } } }`,
			expected: userQuery(``),
		},
		{
			name:     "空not剪枝保留其余条件",
			query:    `query { users(where: { not: {}, name: { eq: "a" } }) { items { id } } }`,
			expected: userQuery(`WHERE "sys_user"."name" = $1`),
			args:     []any{"a"},
		},
		{
			name:     "and列表空对象元素剪枝",
			query:    `query { users(where: { and: [{}, { name: { eq: "a" } }] }) { items { id } } }`,
			expected: userQuery(`WHERE ("sys_user"."name" = $1)`),
			args:     []any{"a"},
		},
		{
			name:     "嵌套全空整体剪枝",
			query:    `query { users(where: { not: { and: [{}] } }) { items { id } } }`,
			expected: userQuery(``),
		},
		{
			name:     "空in列表编译为FALSE",
			query:    `query { users(where: { id: { in: [] } }) { items { id } } }`,
			expected: userQuery(`WHERE FALSE`),
		},
		{
			name:  "嵌套关系空where无悬空AND",
			query: `query { users { items { id posts(where: {}) { title } } } }`,
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
	}
	my.runCases(cases)
}

// TestWhereGuards where参数约束：整体变量无法编译期展开，明确报错防悬空SQL；
// 变更条件剪空后仍强制要求条件
func (my *_DialectSuite) TestWhereGuards() {
	for name, c := range map[string]struct{ query, wants string }{
		"查询整体变量where":   {`query ($w: UserWhereInput) { users(where: $w) { items { id } } }`, "整体变量"},
		"变更整体变量where":   {`mutation ($w: UserWhereInput) { updateUser(input: { name: "x" }, where: $w) { id } }`, "整体变量"},
		"变更空where剪空后报错": {`mutation { updateUser(input: { name: "x" }, where: {}) { id } }`, "需要id或where条件"},
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
