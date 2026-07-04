package pgsql

func (my *_DialectSuite) TestStats() {
	cases := []Case{
		{
			name:  "全表聚合",
			query: `query { userStats { count age { avg max } } }`,
			expected: `SELECT JSONB_BUILD_OBJECT('userStats', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]') AS "json"
					FROM (
						SELECT COUNT(*) AS "count",
							JSONB_BUILD_OBJECT('avg', AVG("sys_user"."age"), 'max', MAX("sys_user"."age")) AS "age"
						FROM "sys_user"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "分组聚合",
			query: `query { userStats(groupBy: ["name"], limit: 10) { key count email { countDistinct } } }`,
			expected: `SELECT JSONB_BUILD_OBJECT('userStats', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]') AS "json"
					FROM (
						SELECT JSONB_BUILD_OBJECT('name', "sys_user"."name") AS "key",
							COUNT(*) AS "count",
							JSONB_BUILD_OBJECT('countDistinct', COUNT(DISTINCT "sys_user"."email")) AS "email"
						FROM "sys_user"
						GROUP BY "sys_user"."name"
						LIMIT 10
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "条件聚合",
			query: `query { userStats(where: { age: { gt: 18 } }) { count } }`,
			args:  []any{int64(18)},
			expected: `SELECT JSONB_BUILD_OBJECT('userStats', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]') AS "json"
					FROM (
						SELECT COUNT(*) AS "count"
						FROM "sys_user"
						WHERE "sys_user"."age" > $1
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "having分组后聚合过滤",
			query: `query { userStats(groupBy: ["name"], having: { count: { gt: 5 }, age: { avg: { ge: 18 } } }) { count } }`,
			args:  []any{int64(5), int64(18)},
			expected: `SELECT JSONB_BUILD_OBJECT('userStats', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]') AS "json"
					FROM (
						SELECT COUNT(*) AS "count"
						FROM "sys_user"
						GROUP BY "sys_user"."name"
						HAVING COUNT(*) > $1 AND AVG("sys_user"."age") >= $2
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "统计与实体查询并存",
			query: `query { userStats { count } users { items { id } } }`,
			expected: `SELECT JSONB_BUILD_OBJECT('userStats', "__sj_0"."json", 'users', "__sj_1"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT COALESCE(JSONB_AGG(TO_JSONB("__sr_0".*)), '[]') AS "json"
					FROM (SELECT COUNT(*) AS "count" FROM "sys_user") AS "__sr_0"
				) AS "__sj_0" ON TRUE
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB("__sr_1".*)), '[]')) AS "json"
					FROM (
						SELECT "sys_user_1"."id" AS "id"
						FROM (SELECT "sys_user"."id" FROM "sys_user" LIMIT 10) AS "sys_user_1"
					) AS "__sr_1"
				) AS "__sj_1" ON TRUE`,
		},
	}
	my.runCases(cases)
}
