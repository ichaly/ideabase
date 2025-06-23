package pgsql

func (my *_DialectSuite) TestBasicQueries() {
	cases := []Case{
		{
			name: "单个用户查询",
			query: `
				query {
					user(id: 1) {
						id
						name
						email
					}
				}
			`,
			expected: `SELECT jsonb_build_object('user', __sj_0.json) AS "__root" 
				FROM (SELECT TRUE) AS "__root_x" 
				LEFT OUTER JOIN LATERAL (
					SELECT COALESCE(jsonb_agg(to_jsonb(__sr_0.*)), '[]') -> 0 AS json 
					FROM (
						SELECT sys_user_0_0.id AS "id",
							sys_user_0_0.name AS "name",
							sys_user_0_0.email AS "email"
						FROM (
							SELECT id, name, email 
							FROM sys_user 
							WHERE id = 1
						) AS sys_user_0_0
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		// {
		// 	name: "用户列表分页查询",
		// 	query: `
		// 		query {
		// 			users(limit: 10, offset: 0) {
		// 				items {
		// 					id
		// 					name
		// 					email
		// 				}
		// 				total
		// 			}
		// 		}
		// 	`,
		// 	expected: `SELECT jsonb_build_object('users', __sj_0.json) AS "__root"
		// 		FROM (SELECT TRUE) AS "__root_x"
		// 		LEFT OUTER JOIN LATERAL (
		// 			SELECT jsonb_build_object(
		// 				'items', COALESCE(jsonb_agg(to_jsonb(__sr_0.*)), '[]'),
		// 				'total', COUNT(*) OVER()
		// 			) AS json
		// 			FROM (
		// 				SELECT sys_user_0_0.id AS "id",
		// 					sys_user_0_0.name AS "name",
		// 					sys_user_0_0.email AS "email"
		// 				FROM (
		// 					SELECT id, name, email
		// 					FROM sys_user
		// 				) AS sys_user_0_0
		// 				LIMIT 10 OFFSET 0
		// 			) AS "__sr_0"
		// 		) AS "__sj_0" ON TRUE`,
		// },
	}
	my.runCases(cases)
}
