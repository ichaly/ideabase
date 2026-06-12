package pgsql

// 排序用例共用的SQL骨架：基础查询里带name列（排序列自动带出）
func sortQuery(clause string) string {
	return `SELECT JSONB_BUILD_OBJECT('users', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
		LEFT OUTER JOIN LATERAL (
			SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]')) AS "json"
			FROM (
				SELECT "sys_user_0"."id" AS "id"
				FROM (SELECT "sys_user"."id", "sys_user"."name" FROM sys_user ` + clause + `) AS "sys_user_0"
			) AS "__sr_0"
		) AS "__sj_0" ON TRUE`
}

func (my *_DialectSuite) TestSort() {
	cases := []Case{
		{
			name:     "单字段降序",
			query:    `query { users(sort: { name: DESC }) { items { id } } }`,
			expected: sortQuery(`ORDER BY "sys_user"."name" DESC`),
		},
		{
			name:     "默认方向为升序",
			query:    `query { users(sort: { name: ASC }) { items { id } } }`,
			expected: sortQuery(`ORDER BY "sys_user"."name" ASC`),
		},
		{
			name:  "多字段混合排序",
			query: `query { users(sort: { name: ASC, age: DESC_NULLS_LAST }) { items { id } } }`,
			expected: `SELECT JSONB_BUILD_OBJECT('users', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]')) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id"
						FROM (SELECT "sys_user"."id", "sys_user"."name", "sys_user"."age" FROM sys_user
							ORDER BY "sys_user"."name" ASC, "sys_user"."age" DESC NULLS LAST) AS "sys_user_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:     "排序与分页组合",
			query:    `query { users(sort: { name: DESC_NULLS_FIRST }, limit: 5, offset: 10) { items { id } } }`,
			expected: sortQuery(`ORDER BY "sys_user"."name" DESC NULLS FIRST LIMIT 5 OFFSET 10`),
		},
		{
			name:     "条件排序组合",
			query:    `query { users(where: { name: { like: "%a%" } }, sort: { name: ASC }) { items { id } } }`,
			expected: sortQuery(`WHERE "sys_user"."name" LIKE $1 ORDER BY "sys_user"."name" ASC`),
			args:     []any{"%a%"},
		},
	}
	my.runCases(cases)
}
