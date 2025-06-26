package pgsql

func (my *_DialectSuite) TestUnifiedArrayQueries() {
	cases := []Case{
		{
			name: "用户查询(id参数) - 统一数组返回",
			query: `
				query {
					users(id: 1) {
						items {
							id
							name
							email
						}
					}
				}
			`,
			expected: `SELECT
	JSONB_BUILD_OBJECT('users', __sj_0."json") AS "__root"
FROM
	(SELECT TRUE) AS "__root_x"
	LEFT OUTER JOIN LATERAL (
		SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB(__sr_0.*)), '[]')) AS "json"
		FROM
			(
				SELECT
					JSONB_BUILD_ARRAY("sys_user_0_0"."id", "sys_user_0_0"."name", "sys_user_0_0"."email") AS items
				FROM
					(SELECT "sys_user"."id" AS "id", "sys_user"."name" AS "name", "sys_user"."email" AS "email" FROM sys_user) AS "sys_user_0_0"
				WHERE
					"sys_user_0_0"."id" = $1
			) AS "__sr_0"
	) AS "__sj_0" ON TRUE`,
		},
		{
			name: "用户查询(where条件) - 统一数组返回",
			query: `
				query {
					users(where: { name: { eq: "test" } }) {
						items {
							id
							name
							email
						}
					}
				}
			`,
			expected: `SELECT
	JSONB_BUILD_OBJECT('users', __sj_0."json") AS "__root"
FROM
	(SELECT TRUE) AS "__root_x"
	LEFT OUTER JOIN LATERAL (
		SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB(__sr_0.*)), '[]')) AS "json"
		FROM
			(
				SELECT
					JSONB_BUILD_ARRAY("sys_user_0_0"."id", "sys_user_0_0"."name", "sys_user_0_0"."email") AS items
				FROM
					(SELECT "sys_user"."id" AS "id", "sys_user"."name" AS "name", "sys_user"."email" AS "email" FROM sys_user) AS "sys_user_0_0"
				WHERE
					"sys_user_0_0"."name" = $1
			) AS "__sr_0"
	) AS "__sj_0" ON TRUE`,
		},
		{
			name: "用户查询(id和where组合) - 统一数组返回",
			query: `
				query {
					users(id: 1, where: { name: { like: "%test%" } }) {
						items {
							id
							name
							email
						}
					}
				}
			`,
			expected: `SELECT
	JSONB_BUILD_OBJECT('users', __sj_0."json") AS "__root"
FROM
	(SELECT TRUE) AS "__root_x"
	LEFT OUTER JOIN LATERAL (
		SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB(__sr_0.*)), '[]')) AS "json"
		FROM
			(
				SELECT
					JSONB_BUILD_ARRAY("sys_user_0_0"."id", "sys_user_0_0"."name", "sys_user_0_0"."email") AS items
				FROM
					(SELECT "sys_user"."id" AS "id", "sys_user"."name" AS "name", "sys_user"."email" AS "email" FROM sys_user) AS "sys_user_0_0"
				WHERE
					("sys_user_0_0"."id" = $1 AND "sys_user_0_0"."name" LIKE $2)
			) AS "__sr_0"
	) AS "__sj_0" ON TRUE`,
		},
		{
			name: "用户查询(带total字段) - 分页查询",
			query: `
				query {
					users(limit: 10) {
						items {
							id
							name
						}
						total
					}
				}
			`,
			expected: `SELECT
	JSONB_BUILD_OBJECT('users', __sj_0."json") AS "__root"
FROM
	(SELECT TRUE) AS "__root_x"
	LEFT OUTER JOIN LATERAL (
		SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB(__sr_0.*)), '[]'), 'total', COUNT(*) OVER()) AS "json"
		FROM
			(
				SELECT
					JSONB_BUILD_ARRAY("sys_user_0_0"."id", "sys_user_0_0"."name") AS items
				FROM
					(SELECT "sys_user"."id" AS "id", "sys_user"."name" AS "name" FROM sys_user) AS "sys_user_0_0"
				LIMIT 10
			) AS "__sr_0"
	) AS "__sj_0" ON TRUE`,
		},
	}
	my.runCases(cases)
}
