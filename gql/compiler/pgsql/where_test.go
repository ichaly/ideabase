package pgsql

// 条件用例共用的SQL骨架：仅WHERE子句与参数不同
func userQuery(where string) string {
	return `SELECT JSONB_BUILD_OBJECT('users', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
		LEFT OUTER JOIN LATERAL (
			SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]')) AS "json"
			FROM (
				SELECT "sys_user_0"."id" AS "id"
				FROM (SELECT "sys_user"."id" FROM sys_user ` + where + `) AS "sys_user_0"
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
