package pgsql

func (my *_DialectSuite) TestSelect() {
	cases := []Case{
		{
			name: "基础字段查询",
			query: `
				query {
					users {
						items {
							id
							name
							email
						}
					}
				}
			`,
			expected: ``,
		},
	}
	my.runCases(cases)
}
